const { api, shortID, setMessage } = window.LockFlow;
const body = document.querySelector("#certificates");
const message = document.querySelector("#message");

function stageClass(stage) {
  if (stage === "completed") return "ok";
  if (stage === "emergency") return "critical";
  if (stage === "cancelled") return "warning";
  return "notice";
}

async function issue(cycleID, button) {
  button.disabled = true;
  try {
    await api(`/api/certificates/${cycleID}/issue`, { method: "POST" });
    setMessage(message, "");
    await load();
  } catch (error) {
    setMessage(message, error.message);
    button.disabled = false;
  }
}

function row(item) {
  const stage = `<span class="status ${stageClass(item.stage)}">${item.stage}</span>`;
  let certificateCell = '<span class="muted">-</span>';
  let actions = "";
  if (item.certificate) {
    certificateCell = `<strong>${item.certificate.number}</strong><div class="muted">${new Date(item.certificate.issued_at).toLocaleString()}</div>`;
    actions = `<a class="button-link" href="/api/certificates/${item.cycle_id}/document" download>Download HTML</a>
               <a class="button-link" href="/api/certificates/${item.cycle_id}/document?format=json" download>JSON</a>`;
  } else {
    certificateCell = item.eligible
      ? '<span class="muted">Ready to issue</span>'
      : `<span class="muted" title="${item.reason.replace(/"/g, "&quot;")}">${item.reason}</span>`;
    actions = `<button data-issue="${item.cycle_id}" ${item.eligible ? "" : "disabled"}>Issue</button>`;
  }
  return `<tr><td>${item.chamber_id}</td><td title="${item.cycle_id}">${shortID(item.cycle_id)} <span class="muted">g${item.generation}</span></td><td>${item.direction}</td><td>${item.vessel_count}</td><td>${stage}</td><td>${certificateCell}</td><td class="actions">${actions}</td></tr>`;
}

async function load() {
  const items = await api("/api/certificates");
  body.innerHTML = items.map(row).join("") || '<tr><td colspan="7" class="muted">No transits recorded yet</td></tr>';
  body.querySelectorAll("button[data-issue]").forEach((button) => {
    button.addEventListener("click", () => issue(button.dataset.issue, button));
  });
}

document.querySelector("#refresh").addEventListener("click", () => load().catch((error) => setMessage(message, error.message)));
load().catch((error) => setMessage(message, error.message));
setInterval(() => load().catch(() => {}), 8000);
