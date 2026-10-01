package certificate

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"

	"github.com/google/uuid"
)

// document is the stable, downloadable representation of a certificate. It is
// a thin envelope that labels the evidence and carries the integrity fields.
type document struct {
	Schema      string      `json:"schema"`
	Number      string      `json:"certificate_number"`
	Fingerprint string      `json:"content_fingerprint_sha256"`
	Certificate Certificate `json:"transit"`
}

// RenderJSON serialises the certificate as an indented, stable download.
func RenderJSON(c *Certificate) ([]byte, error) {
	envelope := document{
		Schema: "lockflow.transit-certificate/v1",
		Number: c.Number, Fingerprint: c.Fingerprint, Certificate: *c,
	}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// DownloadName returns the suggested file name for a certificate download.
func DownloadName(c *Certificate, extension string) string {
	safe := strings.NewReplacer(" ", "_", "/", "_", ":", "_").Replace(c.Number)
	if extension == "" {
		extension = "json"
	}
	return strings.ToLower(safe) + "." + strings.TrimPrefix(extension, ".")
}

var htmlTemplate = template.Must(template.New("certificate").Funcs(template.FuncMap{
	"upper": strings.ToUpper,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Lock transit certificate {{.Number}}</title>
<style>
:root { color-scheme: light; font-family: "Segoe UI", Arial, sans-serif; color:#16212a; }
body { margin:0; background:#eef2f4; padding:32px 16px; }
.sheet { max-width:900px; margin:0 auto; background:#fff; border:1px solid #c7d2d8; border-top:6px solid #1d6f72; padding:40px 48px; }
.head { display:flex; justify-content:space-between; align-items:flex-start; gap:20px; border-bottom:2px solid #e2e9ec; padding-bottom:20px; }
.brand { font-weight:700; font-size:20px; color:#1d6f72; letter-spacing:.5px; }
.brand span { display:inline-block; border:2px solid #1d6f72; padding:2px 7px; margin-right:10px; }
h1 { font-size:22px; margin:26px 0 4px; }
.sub { color:#5a6b75; font-size:13px; margin-bottom:24px; }
.badge { display:inline-block; background:#e3f2f1; color:#13595c; border:1px solid #5bc0be; font-weight:700; padding:6px 12px; font-size:13px; letter-spacing:.5px; }
h2 { font-size:14px; text-transform:uppercase; letter-spacing:1px; color:#1d6f72; margin:30px 0 10px; }
table { width:100%; border-collapse:collapse; font-size:13px; }
th,td { text-align:left; padding:8px 10px; border-bottom:1px solid #e2e9ec; vertical-align:top; }
th { color:#5a6b75; font-weight:600; width:220px; }
.grid { display:grid; grid-template-columns:1fr 1fr; gap:0 40px; }
.code { font-family:ui-monospace,Consolas,monospace; font-size:12px; word-break:break-all; }
.note { margin-top:34px; padding:14px 16px; background:#f4f8f8; border:1px solid #d7e4e4; font-size:12px; color:#40545e; }
.ok { color:#1d6f72; font-weight:700; }
.muted { color:#788891; }
@media print { body { background:#fff; padding:0; } .sheet { border:none; max-width:none; } }
</style></head>
<body><div class="sheet">
  <div class="head"><div class="brand"><span>LF</span>LockFlow</div><div class="badge">CERTIFIED · COMPLETED TRANSIT</div></div>
  <h1>Lock transit electronic certificate</h1>
  <div class="sub">Issued from the authoritative operation journal. Duplicate issuance of the same transit produces this identical document.</div>
  <table>
    <tr><th>Certificate number</th><td class="code">{{.Number}}</td></tr>
    <tr><th>Chamber</th><td>{{.ChamberID}} &nbsp;·&nbsp; generation {{.Generation}}</td></tr>
    <tr><th>Transit cycle</th><td class="code">{{.CycleID}}</td></tr>
    <tr><th>Convoy plan</th><td class="code">{{.PlanID}} &nbsp;(created {{.PlanCreatedAt.Format "2006-01-02 15:04:05 UTC"}})</td></tr>
    <tr><th>Direction</th><td>{{printf "%s" .Direction | upper}}</td></tr>
  </table>

  <h2>Vessels covered</h2>
  <table><thead><tr><th style="width:auto">Vessel</th><th style="width:auto">Call sign</th><th style="width:auto">Length (m)</th></tr></thead><tbody>
  {{range .Vessels}}<tr><td>{{.Name}}</td><td>{{.CallSign}}</td><td>{{.LengthM}}</td></tr>{{end}}
  </tbody></table>

  <h2>Transit timeline</h2>
  <div class="grid">
    <table>
      <tr><th>Entry / admission signal</th><td>{{if .AdmittedAt.IsZero}}<span class="muted">not recorded</span>{{else}}{{.AdmittedAt.Format "2006-01-02 15:04:05 UTC"}}{{end}}</td></tr>
      <tr><th>Release (field handoff)</th><td>{{.ReleasedAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>
      <tr><th>Transit completed</th><td>{{.CompletedAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>
    </table>
    <table>
      <thead><tr><th style="width:auto">Stage</th><th style="width:auto">Entered at</th></tr></thead>
      {{range .StageHistory}}<tr><td>{{.Stage}}</td><td>{{.OccurredAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>{{end}}
    </table>
  </div>

  <h2>Key equipment confirmations</h2>
  {{if .Devices}}
  <table><thead><tr><th style="width:auto">Equipment</th><th style="width:auto">Action</th><th style="width:auto">State</th><th style="width:auto">Issued</th><th style="width:auto">Settled</th></tr></thead><tbody>
  {{range .Devices}}<tr><td>{{.Device}}</td><td>{{.Action}}{{if .Target}} {{.Target}}{{end}}</td><td class="ok">{{.State}}</td><td>{{.IssuedAt.Format "15:04:05"}}</td><td>{{.SettledAt.Format "15:04:05"}}</td></tr>{{end}}
  </tbody></table>
  {{else}}<p class="muted">No settled equipment commands recorded for this transit.</p>{{end}}

  <h2>Anomalous events</h2>
  {{if .Anomalies}}
  <table><thead><tr><th style="width:auto">Event</th><th style="width:auto">Summary</th><th style="width:auto">At</th></tr></thead><tbody>
  {{range .Anomalies}}<tr><td class="code">{{.Type}} (#{{.Sequence}})</td><td>{{.Summary}}</td><td>{{.OccurredAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>{{end}}
  </tbody></table>
  {{else}}<p class="ok">No anomalous events recorded during this transit.</p>{{end}}

  <div class="note">
    Evidence spans journal sequences {{.FirstSequence}}–{{.LastSequence}} for chamber {{.ChamberID}}.
    Completion was driven by a confirmed field signal handoff, which is the only eligible terminal path.
    <br>Content fingerprint (SHA-256): <span class="code">{{.Fingerprint}}</span>
  </div>
</div></body></html>`))

// RenderHTML renders the printable, self-contained certificate sheet.
func RenderHTML(c *Certificate) ([]byte, error) {
	var buffer bytes.Buffer
	if err := htmlTemplate.Execute(&buffer, c); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

var refusalTemplate = template.Must(template.New("refusal").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Certificate refused</title>
<style>
:root { color-scheme: light; font-family: "Segoe UI", Arial, sans-serif; color:#16212a; }
body { margin:0; background:#eef2f4; padding:32px 16px; }
.sheet { max-width:760px; margin:0 auto; background:#fff; border:1px solid #c7d2d8; border-top:6px solid #b0574a; padding:40px 48px; }
.brand { font-weight:700; font-size:20px; color:#1d6f72; }
.brand span { display:inline-block; border:2px solid #1d6f72; padding:2px 7px; margin-right:10px; }
h1 { font-size:21px; margin:26px 0 6px; }
.reason { font-size:14px; margin:8px 0 26px; }
.badge { display:inline-block; background:#f7e6e2; color:#8f3434; border:1px solid #ef6f6c; font-weight:700; padding:6px 12px; font-size:13px; letter-spacing:.5px; }
.code { font-family:ui-monospace,Consolas,monospace; font-size:12px; color:#5a6b75; word-break:break-all; }
.note { margin-top:30px; padding:14px 16px; background:#f4f8f8; border:1px solid #d7e4e4; font-size:12px; color:#40545e; }
a { color:#1d6f72; }
</style></head>
<body><div class="sheet">
  <div class="brand"><span>LF</span>LockFlow</div>
  <h1>Electronic certificate cannot be issued</h1>
  <p class="reason">Transit <span class="code">{{.CycleID}}</span> is not eligible: {{.Reason.Reason}}</p>
  <p><span class="badge">REFUSED · {{.Reason.Code}}</span></p>
  <div class="note">Only a genuinely completed transit whose release was driven by a confirmed field
  signal handoff can be certified. Cancelled, emergency or unfinished transits are refused by policy.</div>
  <p style="margin-top:24px"><a href="/certificates">← Back to certificates</a></p>
</div></body></html>`))

// refusalPage is the template input for an ineligible transit.
type refusalPage struct {
	CycleID string
	Reason  *RejectReason
}

// RenderRefusalHTML renders the operator-facing refusal page.
func RenderRefusalHTML(cycleID uuid.UUID, reason *RejectReason) []byte {
	var buffer bytes.Buffer
	_ = refusalTemplate.Execute(&buffer, refusalPage{CycleID: cycleID.String(), Reason: reason})
	return buffer.Bytes()
}
