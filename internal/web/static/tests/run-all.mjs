// Cross-platform test runner: `node --test <dir>` glob handling is
// inconsistent on Windows, so enumerate the files explicitly. Exit code is
// non-zero when any file fails — CI and `make test` both gate on this.
import { readdir } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const dir = dirname(fileURLToPath(import.meta.url));
const files = (await readdir(dir)).filter(f => f.endsWith('.test.mjs')).sort();
let failed = 0;
for (const f of files) {
    const res = spawnSync(process.execPath, ['--test', join(dir, f)], { stdio: 'inherit' });
    if (res.status !== 0) failed++;
}
console.log(`\n${files.length} test files, ${failed} failing`);
process.exit(failed === 0 ? 0 : 1);
