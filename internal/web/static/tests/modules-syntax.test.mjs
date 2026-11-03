// Guard: every shipped static module must PARSE and every relative
// import must resolve to a file that exports the names it asks for.
// A single lost line marker once shipped a SyntaxError that killed the
// whole module graph and left the dashboard a dead white page (r5g).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');

function moduleFiles() {
    const out = [path.join(root, 'app.js')];
    const jsDir = path.join(root, 'js');
    for (const e of fs.readdirSync(jsDir)) {
        if (e.endsWith('.js')) out.push(path.join(jsDir, e));
    }
    return out;
}

function exportsOf(file) {
    const src = fs.readFileSync(file, 'utf8');
    const names = new Set();
    for (const m of src.matchAll(/export\s+(?:async\s+)?function\s+(\w+)/g)) names.add(m[1]);
    for (const m of src.matchAll(/export\s+(?:const|let|var|class)\s+(\w+)/g)) names.add(m[1]);
    for (const m of src.matchAll(/export\s*\{([^}]+)\}/g)) {
        m[1].split(',').forEach((n) => names.add(n.trim().split(/\s+as\s+/).pop().trim()));
    }
    return names;
}

test('every shipped module parses (node --check)', () => {
    for (const f of moduleFiles()) {
        const tmp = f + '.syntaxcheck.mjs';
        fs.copyFileSync(f, tmp);
        try {
            execFileSync(process.execPath, ['--check', tmp], { stdio: 'pipe' });
        } catch (err) {
            assert.fail(`${path.relative(root, f)} does not parse:\n${err.stderr}`);
        } finally {
            fs.rmSync(tmp, { force: true });
        }
    }
});

test('every relative import resolves to real exports', () => {
    const files = moduleFiles();
    const exportMap = new Map(files.map((f) => [f, exportsOf(f)]));
    for (const f of files) {
        const src = fs.readFileSync(f, 'utf8');
        for (const m of src.matchAll(/import\s*\{([^}]+)\}\s*from\s*['"]([^'"]+)['"]/g)) {
            const spec = m[2];
            if (!spec.startsWith('.')) continue;
            const target = path.resolve(path.dirname(f), spec);
            assert.ok(exportMap.has(target),
                `${path.relative(root, f)} imports missing file ${spec}`);
            const names = exportMap.get(target);
            for (const raw of m[1].split(',')) {
                const n = raw.trim().split(/\s+as\s+/)[0].trim();
                if (!n) continue;
                assert.ok(names.has(n),
                    `${path.relative(root, f)} imports '${n}' which ${spec} does not export`);
            }
        }
    }
});
