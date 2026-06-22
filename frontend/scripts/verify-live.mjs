import { chromium } from 'playwright';
import { PNG } from 'pngjs';

const url = process.env.SCREEN_SERVER_URL ?? 'http://localhost:5173/';

function diffRatio(a, b) {
  let changed = 0;
  const total = Math.min(a.data.length, b.data.length) / 4;
  for (let i = 0; i < total; i++) {
    const o = i * 4;
    const delta =
      Math.abs(a.data[o] - b.data[o]) +
      Math.abs(a.data[o + 1] - b.data[o + 1]) +
      Math.abs(a.data[o + 2] - b.data[o + 2]);
    if (delta > 24) {
      changed++;
    }
  }
  return changed / total;
}

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 });

try {
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => {
    const text = document.body.innerText;
    return text.includes('connected');
  }, { timeout: 20_000 });

  await page.waitForFunction(() => {
    const video = document.querySelector('video');
    const quality = video?.getVideoPlaybackQuality?.();
    return Boolean(video && quality && quality.totalVideoFrames > 20 && video.currentTime > 0.2);
  }, { timeout: 25_000 });

  const viewer = page.locator('.viewer');
  const before = PNG.sync.read(await viewer.screenshot());
  const first = await page.evaluate(() => {
    const video = document.querySelector('video');
    const quality = video.getVideoPlaybackQuality();
    return {
      currentTime: video.currentTime,
      decoded: quality.totalVideoFrames,
      dropped: quality.droppedVideoFrames,
    };
  });

  await page.waitForTimeout(2200);

  const after = PNG.sync.read(await viewer.screenshot());
  const second = await page.evaluate(() => {
    const video = document.querySelector('video');
    const quality = video.getVideoPlaybackQuality();
    return {
      currentTime: video.currentTime,
      decoded: quality.totalVideoFrames,
      dropped: quality.droppedVideoFrames,
    };
  });

  const ratio = diffRatio(before, after);
  console.log(JSON.stringify({ first, second, screenshotDiffRatio: ratio }, null, 2));

  if (second.decoded <= first.decoded + 90) {
    throw new Error(`decoded frames did not increase enough: ${first.decoded} -> ${second.decoded}`);
  }
  if (second.currentTime <= first.currentTime + 0.5) {
    throw new Error(`video currentTime did not advance enough: ${first.currentTime} -> ${second.currentTime}`);
  }
  if (ratio < 0.0001) {
    throw new Error(`viewer screenshot did not change enough: ratio=${ratio}`);
  }
} finally {
  await browser.close();
}
