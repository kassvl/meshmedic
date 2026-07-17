// Re-shoot single clips against the current state: node reshoot.mjs <name> <url> <seconds>
import { chromium } from "playwright-core";
import { mkdirSync, renameSync, readdirSync } from "node:fs";
import path from "node:path";

const [name, url, seconds] = process.argv.slice(2);
const OUT = path.resolve("clips");
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({ channel: "chrome", headless: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 810 },
  recordVideo: { dir: OUT, size: { width: 1440, height: 810 } },
});
const page = await context.newPage();
await page.goto(url, { waitUntil: "networkidle" }).catch(() => {});
await new Promise((r) => setTimeout(r, Number(seconds) * 1000));
await context.close();
await browser.close();
const raw = readdirSync(OUT).find((f) => f.endsWith(".webm") && !f.startsWith("clip-"));
renameSync(path.join(OUT, raw), path.join(OUT, `clip-${name}.webm`));
console.log(`recorded clip-${name}.webm`);
