package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type flowClient struct {
	t      *testing.T
	server *httptest.Server
}

func newFlowServer(t *testing.T, dataDir string) (*Runtime, *flowClient) {
	t.Helper()
	runtime, err := NewRuntime(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	server := httptest.NewServer(Router(runtime, filepath.Join("..", "..", "web")))
	t.Cleanup(server.Close)
	return runtime, &flowClient{t: t, server: server}
}

func (c *flowClient) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	status, decoded := c.doAny(method, path, body)
	result, _ := decoded.(map[string]any)
	return status, result
}

func (c *flowClient) doAny(method, path string, body any) (int, any) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, c.server.URL+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.StatusCode, decoded
}

func (c *flowClient) raw(method, path string) (int, http.Header, string) {
	c.t.Helper()
	request, err := http.NewRequest(method, c.server.URL+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := new(bytes.Buffer)
	_, _ = buffer.ReadFrom(response.Body)
	return response.StatusCode, response.Header, buffer.String()
}

// driveTransit runs one chamber through the real operator endpoints all the
// way to a signal-handoff completion and returns the cycle id.
func (c *flowClient) driveTransit(chamberID, direction string) string {
	status, body := c.do(http.MethodPost, "/api/plans", map[string]any{
		"chamber_id": chamberID, "direction": direction,
		"vessels": []map[string]any{{"name": "River Star", "call_sign": "RS-204", "length_m": 86}},
	})
	if status != http.StatusCreated {
		c.t.Fatalf("create plan: %d %v", status, body)
	}
	cycle := body["cycle"].(map[string]any)
	cycleID := cycle["id"].(string)

	gateStep := func(direction, action string) {
		status, gateBody := c.do(http.MethodPost, "/api/chambers/"+chamberID+"/gates/"+direction+"/"+action, nil)
		if status != http.StatusAccepted {
			c.t.Fatalf("gate %s %s: %d %v", direction, action, status, gateBody)
		}
		commandID := gateBody["id"].(string)
		status, ackBody := c.do(http.MethodPost, "/api/chambers/"+chamberID+"/commands/"+commandID+"/ack", nil)
		if status != http.StatusOK {
			c.t.Fatalf("ack %s %s: %d %v", direction, action, status, ackBody)
		}
		// Release the open-gate reservation so the opposed gate can open next.
		_, _ = c.do(http.MethodPost, "/api/chambers/"+chamberID+"/reservation/release", nil)
	}
	gateStep("upstream", "open")
	gateStep("upstream", "close")
	gateStep("downstream", "open")
	gateStep("downstream", "close")

	status, handoffBody := c.do(http.MethodPost, "/api/chambers/"+chamberID+"/signals/handoff", map[string]any{
		"cycle_id": cycleID, "generation": 1,
	})
	if status != http.StatusOK {
		c.t.Fatalf("signal handoff: %d %v", status, handoffBody)
	}
	return cycleID
}

func TestCertificateEndToEndFlow(t *testing.T) {
	_, client := newFlowServer(t, "")
	cycleID := client.driveTransit("chamber-1", "upstream")

	// The completed transit is listed as eligible before issuance.
	if status := certificatesStatus(client, cycleID); status != "completed" {
		t.Fatalf("expected completed stage in certificate list, got %q", status)
	}

	// First issue creates the voucher.
	status, issueBody := client.do(http.MethodPost, "/api/certificates/"+cycleID+"/issue", nil)
	if status != http.StatusCreated {
		t.Fatalf("issue: %d %v", status, issueBody)
	}
	cert := issueBody["certificate"].(map[string]any)
	number := cert["number"].(string)
	if !strings.HasPrefix(number, "LFC-") {
		t.Fatalf("unexpected number %q", number)
	}
	if vessels := cert["vessels"].([]any); len(vessels) != 1 {
		t.Fatalf("expected 1 vessel, got %d", len(vessels))
	}
	if devices := cert["devices"].([]any); len(devices) != 4 {
		t.Fatalf("expected 4 gate confirmations, got %d", len(devices))
	}
	if cert["signal_handoff_at"] == nil || cert["completed_at"] == nil || cert["admitted_at"] == nil {
		t.Fatal("certificate must carry admission, completion and handoff times")
	}

	// Second issue is idempotent.
	status, againBody := client.do(http.MethodPost, "/api/certificates/"+cycleID+"/issue", nil)
	if status != http.StatusOK || againBody["already_issued"] != true {
		t.Fatalf("idempotent issue expected 200 already_issued, got %d %v", status, againBody)
	}
	if againBody["certificate"].(map[string]any)["number"] != number {
		t.Fatal("idempotent re-issue returned a different voucher number")
	}

	// JSON detail and both download formats.
	if status, detail := client.do(http.MethodGet, "/api/certificates/"+cycleID, nil); status != http.StatusOK ||
		detail["number"] != number {
		t.Fatalf("detail: %d %v", status, detail)
	}
	status, headers, html := client.raw(http.MethodGet, "/api/certificates/"+cycleID+"/document")
	if status != http.StatusOK || !strings.Contains(headers.Get("Content-Type"), "text/html") {
		t.Fatalf("html document: %d %s", status, headers.Get("Content-Type"))
	}
	if !strings.Contains(html, number) || !strings.Contains(html, "Lock Transit Completion Certificate") {
		t.Fatal("downloaded document missing voucher identity")
	}
	if !strings.Contains(headers.Get("Content-Disposition"), strings.ToLower(number)+".html") {
		t.Fatalf("download filename missing: %q", headers.Get("Content-Disposition"))
	}
	status, headers, jsonDoc := client.raw(http.MethodGet, "/api/certificates/"+cycleID+"/document?format=json")
	if status != http.StatusOK || !strings.Contains(headers.Get("Content-Type"), "application/json") ||
		!strings.Contains(jsonDoc, number) {
		t.Fatalf("json document: %d %s", status, headers.Get("Content-Type"))
	}
}

func TestCertificateRefusesCancelledAndEmergency(t *testing.T) {
	_, client := newFlowServer(t, "")

	// Cancelled transit.
	status, planBody := client.do(http.MethodPost, "/api/plans", map[string]any{
		"chamber_id": "chamber-1", "direction": "downstream",
		"vessels": []map[string]any{{"name": "Night Ferry", "call_sign": "NF-9", "length_m": 60}},
	})
	if status != http.StatusCreated {
		t.Fatalf("create plan: %d %v", status, planBody)
	}
	planID := planBody["plan"].(map[string]any)["id"].(string)
	cancelledCycle := planBody["cycle"].(map[string]any)["id"].(string)
	if cancelStatus, cancelBody := client.do(http.MethodPost, "/api/plans/"+planID+"/cancel", nil); cancelStatus != http.StatusOK {
		t.Fatalf("cancel plan: %d %v", cancelStatus, cancelBody)
	}
	var body map[string]any
	status, body = client.do(http.MethodPost, "/api/certificates/"+cancelledCycle+"/issue", nil)
	if status != http.StatusConflict || !strings.Contains(body["reason"].(string), "cancelled") {
		t.Fatalf("cancelled refusal expected 409 with reason, got %d %v", status, body)
	}

	// Emergency transit.
	status, planBody = client.do(http.MethodPost, "/api/plans", map[string]any{
		"chamber_id": "chamber-2", "direction": "upstream",
		"vessels": []map[string]any{{"name": "Dawn Treader", "call_sign": "DT-1", "length_m": 72}},
	})
	if status != http.StatusCreated {
		t.Fatalf("create emergency plan: %d %v", status, planBody)
	}
	emergencyCycle := planBody["cycle"].(map[string]any)["id"].(string)
	if emergencyStatus, emergencyBody := client.do(http.MethodPost, "/api/chambers/chamber-2/emergency", nil); emergencyStatus != http.StatusOK {
		t.Fatalf("emergency stop: %d %v", emergencyStatus, emergencyBody)
	}
	status, body = client.do(http.MethodPost, "/api/certificates/"+emergencyCycle+"/issue", nil)
	if status != http.StatusConflict || !strings.Contains(body["reason"].(string), "emergency") {
		t.Fatalf("emergency refusal expected 409 with reason, got %d %v", status, body)
	}
}

func TestUnknownTransitAndNotIssued(t *testing.T) {
	_, client := newFlowServer(t, "")
	unknown := "00000000-0000-0000-0000-000000000000"
	if status, _ := client.do(http.MethodPost, "/api/certificates/"+unknown+"/issue", nil); status != http.StatusNotFound {
		t.Fatalf("unknown issue expected 404, got %d", status)
	}

	// A transit exists but has no voucher yet.
	cycleID := client.driveTransit("chamber-1", "upstream")
	if status, _ := client.do(http.MethodGet, "/api/certificates/"+cycleID, nil); status != http.StatusConflict {
		t.Fatalf("not-issued detail expected 409, got %d", status)
	}
}

func TestCertificatePersistsAcrossRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "journal")
	_, client := newFlowServer(t, dataDir)
	cycleID := client.driveTransit("chamber-1", "upstream")
	status, body := client.do(http.MethodPost, "/api/certificates/"+cycleID+"/issue", nil)
	if status != http.StatusCreated {
		t.Fatalf("issue: %d %v", status, body)
	}
	number := body["certificate"].(map[string]any)["number"].(string)
	client.server.Close()

	// Rebuild the whole runtime over the file journal and expect the voucher
	// and its non-repeatable number to survive.
	_, restarted := newFlowServer(t, dataDir)
	status, reloaded := restarted.do(http.MethodGet, "/api/certificates/"+cycleID, nil)
	if status != http.StatusOK || reloaded["number"] != number {
		t.Fatalf("voucher did not survive restart: %d %v", status, reloaded)
	}
	status, again := restarted.do(http.MethodPost, "/api/certificates/"+cycleID+"/issue", nil)
	if status != http.StatusOK || again["already_issued"] != true {
		t.Fatalf("post-restart issue must stay idempotent: %d %v", status, again)
	}
}

// certificatesStatus peeks the certificates list for the given cycle.
func certificatesStatus(client *flowClient, cycleID string) string {
	status, decoded := client.doAny(http.MethodGet, "/api/certificates", nil)
	if status != http.StatusOK {
		client.t.Fatalf("list: %d", status)
	}
	for _, item := range decoded.([]any) {
		entry := item.(map[string]any)
		if entry["cycle_id"] == cycleID {
			return entry["stage"].(string)
		}
	}
	return ""
}
