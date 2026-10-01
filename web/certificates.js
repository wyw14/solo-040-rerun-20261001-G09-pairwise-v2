const { api, shortID, setMessage, text } = window.LockFlow;
const rows = document.querySelector("#rows");
const message = document.querySelector("#message");
const count = document.querySelector("#count");
const lookupForm = document.querySelector("#lookup-form");
const lookupMessage = document.querySelector("#lookup-message");
const lookupResult = document.querySelector("#lookup-result");

const stageClass = (stage) => {
  if (stage === "completed") return "ok";
  if (stage === "emergency") return "critical";
  if (stage === "cancelled") return "warning";
  return "";
};

const renderOutcome = (item) => {
  if (item.certificate) {
    return `<a href="/certificates/${item.cycle_id}" title="Open printable certificate"><span class="status ok">${item.certificate.number}</span></a>`;
  }
  if (item.reject) {
    return `<span class="status ${item.reject.code === "emergency_terminated" ? "critical" : "warning"}" title="${item.reject.reason}">${item.reject.code}</span>`;
  }
  return `<span class="muted">in progress — not eligible</span>`;
};

const renderActions = (item) => {
  if (!item.certificate) return "";
  return `<a class="action-link" href="/certificates/${item.cycle_id}" target="_blank" rel="noopener">View</a>
    <a class="action-link" href="/api/certificates/${item.cycle_id}/download">Download JSON</a>`;
};

async function load() {
  const items = await api("/api/certificates");
  count.textContent = `${items.length} transit(s)`;
  rows.innerHTML = items.map((item) => `
    <tr>
      <td>${item.chamber_id}<div class="muted">gen ${item.generation}</div></td>
      <td class="code-cell" title="${item.cycle_id}">${shortID(item.cycle_id)}</td>
      <td>${item.direction}</td>
      <td><span class="status ${stageClass(item.stage)}">${item.stage}</span>${item.reject?.reason ? `<div class="muted">${item.reject.reason}</div>` : ""}</td>
      <td>${renderOutcome(item)}</td>
      <td>${item.stage === "completed" ? new Date(item.updated_at).toLocaleString() : "-"}</td>
      <td>${renderActions(item)}</td>
    </tr>`).join("") || '<tr><td colspan="7" class="muted">No transits recorded yet</td></tr>';
  setMessage(message, "");
}

const renderIssued = (issuance) => {
  const cert = issuance.certificate;
  lookupResult.innerHTML = `
    <section class="cert-card">
      <div><span class="status ok">CERTIFIED</span> <strong>${cert.number}</strong></div>
      <table>
        <tr><th>Chamber</th><td>${cert.chamber_id} · generation ${cert.generation}</td></tr>
        <tr><th>Direction</th><td>${cert.direction}</td></tr>
        <tr><th>Vessels</th><td>${cert.vessels.map((v) => `${v.name} (${v.call_sign})`).join(", ")}</td></tr>
        <tr><th>Entry</th><td>${cert.admitted_at ? new Date(cert.admitted_at).toLocaleString() : "-"}</td></tr>
        <tr><th>Release</th><td>${new Date(cert.released_at).toLocaleString()}</td></tr>
        <tr><th>Equipment confirmations</th><td>${cert.devices.length} settled command(s)</td></tr>
        <tr><th>Anomalies</th><td>${cert.anomalies.length ? cert.anomalies.map((a) => a.summary).join("; ") : "none"}</td></tr>
        <tr><th>Fingerprint</th><td class="code-cell">${cert.fingerprint}</td></tr>
      </table>
      <div class="cert-actions">
        <a href="/certificates/${cert.cycle_id}" target="_blank" rel="noopener"><button type="button">Open certificate</button></a>
        <a href="/api/certificates/${cert.cycle_id}/download"><button type="button">Download JSON</button></a>
      </div>
    </section>`;
};

lookupForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const cycleID = document.querySelector("#cycle-id").value.trim();
  if (!cycleID) {
    setMessage(lookupMessage, "Enter a transit cycle identifier.");
    return;
  }
  lookupResult.innerHTML = "";
  try {
    const issuance = await api(`/api/certificates/${encodeURIComponent(cycleID)}`);
    setMessage(lookupMessage, "");
    renderIssued(issuance);
  } catch (error) {
    setMessage(lookupMessage, `Certificate refused: ${error.message}`);
  }
});

load().catch((error) => setMessage(message, error.message));
setInterval(() => load().catch(() => {}), 10000);
