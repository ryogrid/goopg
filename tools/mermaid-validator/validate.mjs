#!/usr/bin/env node
/**
 * Validate every Mermaid diagram in a set of Markdown files or a directory.
 *
 * Launches a headless Chromium browser, loads each diagram with mermaid.parse(),
 * and reports any parse errors. This reproduces GitHub's client-side rendering
 * pipeline (mermaid.js in a browser) and catches syntax errors that a static
 * linter cannot detect.
 *
 * Usage:
 *   node validate.mjs                     # validate docs/wiki/ (default)
 *   node validate.mjs path/to/file.md     # single file
 *   node validate.mjs path/to/dir/        # all .md files recursively
 */

import puppeteer from 'puppeteer';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// --- collect files ----------------------------------------------------------
const startPath = process.argv[2]
  ? path.resolve(process.argv[2])
  : path.resolve(__dirname, '../../docs/wiki');

const files = [];
function collect(p) {
  if (!fs.existsSync(p)) {
    console.error(`ERROR: path not found: ${p}`);
    process.exit(1);
  }
  const st = fs.statSync(p);
  if (st.isFile() && p.endsWith('.md')) {
    files.push(p);
  } else if (st.isDirectory()) {
    for (const e of fs.readdirSync(p)) {
      collect(path.join(p, e));
    }
  }
}
collect(startPath);

// --- extract mermaid blocks -------------------------------------------------
const blocks = [];
for (const f of files) {
  const content = fs.readFileSync(f, 'utf-8');
  const lines = content.split('\n');
  let inMermaid = false, block = [], lineNo = 0;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim().startsWith('```mermaid')) {
      inMermaid = true; block = []; lineNo = i + 1;
    } else if (inMermaid && lines[i].trim().startsWith('```')) {
      inMermaid = false;
      blocks.push({ file: f, line: lineNo, diagram: block.join('\n') });
    } else if (inMermaid) {
      block.push(lines[i]);
    }
  }
}

// --- launch browser & validate ----------------------------------------------
const mermaidPath = path.resolve(__dirname, 'node_modules/mermaid/dist/mermaid.min.js');
if (!fs.existsSync(mermaidPath)) {
  console.error('ERROR: mermaid not found. Run `npm install` first.');
  process.exit(1);
}

const browser = await puppeteer.launch({ headless: 'new' });
const page = await browser.newPage();
await page.setContent('<!DOCTYPE html><html><body></body></html>');
await page.addScriptTag({ path: mermaidPath });
await page.evaluate(() => mermaid.initialize({ startOnLoad: false, securityLevel: 'loose' }));

let pass = 0, fail = 0;
const errors = [];
for (const b of blocks) {
  const rel = path.relative(process.cwd(), b.file);
  const result = await page.evaluate((diagram) => {
    return new Promise((resolve) => {
      try {
        mermaid.parse(diagram).then(() => resolve({ ok: true }))
          .catch(e => resolve({ ok: false, msg: String(e.message || e).substring(0, 200) }));
      } catch (e) {
        resolve({ ok: false, msg: String(e.message || e).substring(0, 200) });
      }
    });
  }, b.diagram);
  if (result.ok) {
    pass++;
  } else {
    fail++;
    errors.push(`${rel}:${b.line} — ${result.msg}`);
  }
}
await browser.close();

console.log(`Total: ${blocks.length}  Pass: ${pass}  Fail: ${fail}`);
if (errors.length) {
  console.log('=== FAILURES ===');
  for (const e of errors) console.log(e);
  process.exit(1);
}