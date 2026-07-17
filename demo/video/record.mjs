// Records the M1 demo as five real clips using the system Chrome.
// Prereqs: demo cluster healthy at 80/20 (scripts/reset-take.sh),
// port-forwards for Prometheus :9090 and Grafana :3000, GITHUB_TOKEN set,
// and the meshmedic binary built (MESHMEDIC_BIN, default ../../meshmedic).
//
// Every frame is real: the clips bracket the waiting (rate windows, the
// 90s for-duration, Argo sync) instead of speeding footage up.
import { chromium } from "playwright-core";
import { execSync, spawn } from "node:child_process";
import { mkdirSync, renameSync, readdirSync } from "node:fs";
import path from "node:path";

const OUT = path.resolve("clips");
const GRAFANA = "http://localhost:3000/d/meshmedic-demo/?kiosk";
const PROM = "http://127.0.0.1:9090/api/v1/query";
const REPO = "kassvl/meshmedic-demo-config";
const BIN = process.env.MESHMEDIC_BIN || path.resolve("../../meshmedic");

const sh = (cmd) => execSync(cmd, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }).trim();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function promql(query) {
  const res = await fetch(`${PROM}?query=${encodeURIComponent(query)}`);
  const body = await res.json();
  return body.data.result.length ? parseFloat(body.data.result[0].value[1]) : NaN;
}

const P99_V2 = `histogram_quantile(0.99, sum by (le) (rate(istio_request_duration_milliseconds_bucket{reporter="waypoint", destination_service_name="payments", destination_version="v2"}[1m])))`;
const RPS_V2 = `sum(rate(istio_requests_total{reporter="waypoint", destination_service_name="payments", destination_version="v2"}[1m]))`;

async function waitFor(desc, cond, intervalMs = 10000) {
  process.stdout.write(`waiting: ${desc} `);
  for (;;) {
    if (await cond()) break;
    process.stdout.write(".");
    await sleep(intervalMs);
  }
  console.log(" ok");
}

async function clip(name, seconds, urls) {
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 810 },
    recordVideo: { dir: OUT, size: { width: 1440, height: 810 } },
  });
  const page = await context.newPage();
  for (const [i, u] of urls.entries()) {
    await page.goto(u, { waitUntil: "networkidle" }).catch(() => {});
    await sleep((seconds * 1000) / urls.length - (i === 0 ? 0 : 500));
    if (u.includes("github.com") && urls.length === 1) {
      // One slow scroll so the report body is readable on camera.
      await page.mouse.wheel(0, 500);
      await sleep(1500);
      await page.mouse.wheel(0, 600);
      await sleep(1500);
    }
  }
  await context.close();
  await browser.close();
  const raw = readdirSync(OUT).find((f) => f.endsWith(".webm") && !f.startsWith("clip-"));
  renameSync(path.join(OUT, raw), path.join(OUT, `clip-${name}.webm`));
  console.log(`recorded clip-${name}.webm`);
}

mkdirSync(OUT, { recursive: true });

// Shot 1: healthy mesh, 80/20 split visible.
await clip("1-healthy", 9, [GRAFANA]);

// Chaos, off camera; wait until the p99 panel is visibly on fire.
console.log(sh("../scripts/inject-canary-latency.sh"));
const watch = spawn(BIN, ["watch", "--config", "watch.yaml", "--catalog", "../catalog"], {
  cwd: path.resolve(".."),
  env: process.env,
  stdio: ["ignore", "pipe", "pipe"],
});
let watchLog = "";
watch.stdout.on("data", (d) => (watchLog += d));
watch.stderr.on("data", (d) => (watchLog += d));

await waitFor("p99(v2) above threshold", async () => (await promql(P99_V2)) > 1000);

// Shot 2: the dashboard showing the regression.
await clip("2-breach", 9, [GRAFANA]);

// Shot 3: MeshMedic's pull request.
await waitFor("MeshMedic opens the PR", async () => /opened https/.test(watchLog), 5000);
const prUrl = watchLog.match(/opened (https:\S+)/)[1];
console.log("PR:", prUrl);
await clip("3-pr", 14, [prUrl]);

// Shot 4: the merge. Give GitHub time to show the purple badge.
sh(`gh pr merge ${prUrl.split("/").pop()} --repo ${REPO} --squash`);
await sleep(12000);
await clip("4-merged", 7, [prUrl]);

// Shot 5: canary drained, mesh healed.
await waitFor("canary drains to zero", async () => (await promql(RPS_V2)) < 0.05);
await clip("5-healed", 14, [GRAFANA]);

watch.kill();
console.log("all clips recorded in", OUT);
