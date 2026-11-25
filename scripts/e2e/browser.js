// Drives germ's browser viewer for scripts/e2e/browser.sh: reads commands,
// one per line, from stdin. Needs playwright-core (PLAYWRIGHT_CORE: its
// path, if it isn't installed where node finds it) and a Chromium
// (CHROME: its executable, if playwright's own isn't installed).
const { chromium } = require(process.env.PLAYWRIGHT_CORE || 'playwright-core');
const readline = require('readline');
(async () => {
  const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
  const page = await browser.newPage({ viewport: { width: 1200, height: 700 } });
  const rl = readline.createInterface({ input: process.stdin });
  for await (const line of rl) {
    const [cmd, ...rest] = line.split(' ');
    const arg = rest.join(' ');
    try {
      if (cmd === 'open') await page.goto(arg);
      else if (cmd === 'name') { await page.fill('#name', arg); await page.press('#name', 'Enter'); }
      else if (cmd === 'term') await page.click('.xterm');
      else if (cmd === 'key') await page.keyboard.press(arg);
      else if (cmd === 'type') await page.keyboard.type(arg, { delay: 20 });
      else if (cmd === 'click') await page.click(arg);
      else if (cmd === 'shot') await page.screenshot({ path: arg });
      else if (cmd === 'quit') break;
    } catch (e) { console.log('error', line, String(e).split('\n')[0]); }
  }
  await browser.close();
})();
