package certificate

import (
	"bytes"
	"errors"
	"html/template"
	"strings"
)

// ErrNotFound indicates the transit cycle has no journal evidence.
var ErrNotFound = errors.New("transit cycle not found")

// ErrNotIssued indicates the transit exists but has no certificate yet.
var ErrNotIssued = errors.New("no certificate issued for this transit")

// Document renders an issued certificate into a downloadable, self-contained
// HTML document.
func Document(cert Certificate) ([]byte, error) {
	var buffer bytes.Buffer
	if err := certificateTemplate.Execute(&buffer, newTemplateData(cert)); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// DownloadName is the suggested file name for the HTML document.
func DownloadName(cert Certificate) string {
	return strings.ToLower(cert.Number) + ".html"
}

type templateDevice struct {
	Device    string
	Action    string
	State     string
	QueuedAt  string
	SettledAt string
}

type templateIncident struct {
	Severity   string
	Summary    string
	EventType  string
	OccurredAt string
	Note       string
}

type templateData struct {
	Certificate
	VesselNames string
	Devices     []templateDevice
	Incidents   []templateIncident
	Transitions []StageTransition
}

func newTemplateData(cert Certificate) templateData {
	devices := make([]templateDevice, 0, len(cert.Devices))
	for _, device := range cert.Devices {
		item := templateDevice{
			Device: string(device.Device), Action: device.Action, State: string(device.State),
			QueuedAt: device.QueuedAt.Format("2006-01-02 15:04:05 MST"),
		}
		if device.SettledAt != nil {
			item.SettledAt = device.SettledAt.Format("2006-01-02 15:04:05 MST")
		}
		devices = append(devices, item)
	}
	incidents := make([]templateIncident, 0, len(cert.Incidents))
	for _, incident := range cert.Incidents {
		incidents = append(incidents, templateIncident{
			Severity: incident.Severity, Summary: incident.Summary,
			EventType:  string(incident.EventType),
			OccurredAt: incident.OccurredAt.Format("2006-01-02 15:04:05 MST"),
			Note:       incident.Note,
		})
	}
	names := make([]string, 0, len(cert.Vessels))
	for _, vessel := range cert.Vessels {
		names = append(names, vessel.Name+" ("+vessel.CallSign+")")
	}
	return templateData{
		Certificate: cert, VesselNames: strings.Join(names, ", "),
		Devices: devices, Incidents: incidents,
		Transitions: cert.Transitions,
	}
}

var certificateTemplate = template.Must(template.New("certificate").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Lock transit certificate {{.Number}}</title>
<style>
  :root { color-scheme: light; font-family: "Segoe UI", Arial, sans-serif; color: #16232b; }
  body { margin: 0; background: #eef2f4; padding: 32px 16px; }
  .sheet { max-width: 880px; margin: 0 auto; background: #fff; border: 1px solid #c6d2d8;
    border-top: 6px solid #1d6f72; padding: 40px 48px; box-shadow: 0 4px 24px rgba(0,0,0,.08); }
  .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; color: #1d6f72; letter-spacing: .5px; }
  .brand-mark { width: 28px; height: 28px; border: 2px solid #1d6f72; display: grid; place-items: center; font-size: 12px; }
  h1 { font-size: 24px; margin: 26px 0 4px; }
  .sub { color: #5a6b74; font-size: 14px; margin-bottom: 26px; }
  .number { float: right; text-align: right; }
  .number strong { display: block; font-size: 18px; color: #1d6f72; }
  .number span { font-size: 12px; color: #5a6b74; word-break: break-all; }
  h2 { font-size: 14px; text-transform: uppercase; letter-spacing: 1px; color: #1d6f72;
    margin: 28px 0 10px; border-bottom: 1px solid #d8e0e4; padding-bottom: 6px; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #e3eaed; vertical-align: top; }
  th { width: 34%; color: #5a6b74; font-weight: 600; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0 24px; }
  .badge { display: inline-block; padding: 2px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .badge.ok { background: #dff3f1; color: #17645f; }
  .badge.critical { background: #fde2e1; color: #8f3434; }
  .badge.warning { background: #fdf0d2; color: #7a5a12; }
  .badge.notice { background: #e6edf1; color: #43545e; }
  .muted { color: #5a6b74; }
  .seal { margin-top: 36px; display: flex; justify-content: space-between; align-items: end;
    color: #43545e; font-size: 12px; border-top: 2px solid #1d6f72; padding-top: 14px; }
  .hash { font-family: ui-monospace, "Cascadia Mono", monospace; font-size: 11px; word-break: break-all; }
  @media print { body { background: #fff; padding: 0; } .sheet { border: none; box-shadow: none; } }
</style></head>
<body><div class="sheet">
  <div class="number"><strong>{{.Number}}</strong><span>SHA-256<br><span class="hash">{{.ContentHash}}</span></span></div>
  <div class="brand"><span class="brand-mark">LF</span> LockFlow Transit Authority</div>
  <h1>Lock Transit Completion Certificate</h1>
  <div class="sub">Generated from the authoritative event journal · non-repeatable electronic voucher</div>

  <h2>Convoy plan</h2>
  <table>
    <tr><th>Chamber</th><td>{{.ChamberID}}</td></tr>
    <tr><th>Plan ID</th><td>{{.PlanID}}</td></tr>
    <tr><th>Direction</th><td>{{.Direction}}</td></tr>
    <tr><th>Vessels</th><td>{{.VesselNames}}</td></tr>
    <tr><th>Plan created</th><td>{{.PlanCreatedAt.Format "2006-01-02 15:04:05 MST"}}</td></tr>
  </table>

  <h2>Transit timeline</h2>
  <table>
    <tr><th>Cycle / generation</th><td>{{.CycleID}} (gen {{.Generation}})</td></tr>
    <tr><th>Vessel admitted to lock</th><td>{{.AdmittedAt.Format "2006-01-02 15:04:05 MST"}}</td></tr>
    <tr><th>Release started</th><td>{{.ReleasedAt.Format "2006-01-02 15:04:05 MST"}}</td></tr>
    <tr><th>Field signal handoff confirmed</th><td>{{.SignalHandoffAt.Format "2006-01-02 15:04:05 MST"}}</td></tr>
    <tr><th>Vessel released from lock</th><td>{{.CompletedAt.Format "2006-01-02 15:04:05 MST"}}</td></tr>
  </table>

  <h2>Key equipment confirmations</h2>
  {{if .Devices}}
  <table><thead><tr><th style="width:auto">Device</th><th style="width:auto">Action</th><th style="width:auto">State</th><th style="width:auto">Queued</th><th style="width:auto">Settled</th></tr></thead><tbody>
  {{range .Devices}}<tr><td>{{.Device}}</td><td>{{.Action}}</td><td><span class="badge ok">{{.State}}</span></td><td>{{.QueuedAt}}</td><td>{{if .SettledAt}}{{.SettledAt}}{{else}}<span class="muted">-</span>{{end}}</td></tr>{{end}}
  </tbody></table>
  {{else}}<p class="muted">No equipment commands were recorded.</p>{{end}}

  <h2>Exceptional events</h2>
  {{if .Incidents}}
  <table><thead><tr><th style="width:auto">Severity</th><th style="width:auto">Event</th><th style="width:auto">Time</th><th style="width:30%">Detail</th></tr></thead><tbody>
  {{range .Incidents}}<tr><td><span class="badge {{.Severity}}">{{.Severity}}</span></td><td>{{.Summary}}<div class="muted">{{.EventType}}</div></td><td>{{.OccurredAt}}</td><td>{{if .Note}}{{.Note}}{{else}}<span class="muted">-</span>{{end}}</td></tr>{{end}}
  </tbody></table>
  {{else}}<p><span class="badge ok">none</span> <span class="muted">No exceptional events were recorded during this transit.</span></p>{{end}}

  <div class="seal">
    <div>Issued {{.IssuedAt.Format "2006-01-02 15:04:05 MST"}}<br>Evidence events #{{index .EventRange 0}}–{{index .EventRange 1}} of chamber {{.ChamberID}}</div>
    <div style="text-align:right">LockFlow electronic seal<br><span class="hash">{{.ContentHash}}</span></div>
  </div>
</div></body></html>
`))
