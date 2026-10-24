// Format touched Go files, preserving the project's untouched legacy baseline.
// Node is already installed for UI tests; no additional packages are needed.
const { spawnSync } = require('node:child_process');
const { existsSync } = require('node:fs');

function git(args, optional = false) {
  const result = spawnSync('git', args, { encoding: 'utf8', windowsHide: true });
  if (result.error || result.status !== 0) {
    if (optional) return null;
    throw result.error || new Error(result.stderr || `git ${args.join(' ')} failed`);
  }
  return result.stdout;
}

const head = process.env.FORMAT_HEAD || 'HEAD';
let base = process.env.FORMAT_BASE;
if (!base || /^0+$/.test(base)) {
  // First branch push: compare with its fork point on the default branch.
  // Manual/default-branch runs compare the latest commit with its parent.
  const branch = `origin/${process.env.DEFAULT_BRANCH || 'master'}`;
  base = git(['merge-base', branch, head], true)?.trim();
  const resolvedHead = git(['rev-parse', head]).trim();
  if (!base || base === resolvedHead) base = git(['rev-parse', `${head}^`], true)?.trim();
}
const changed = base
  ? git(['diff', '--name-only', '--diff-filter=ACMR', '-z', base, head, '--', '*.go'])
  : git(['ls-files', '-z', '--', '*.go']);
const files = changed.split('\0').filter(file => file && existsSync(file));
if (files.length === 0) {
  console.log('No changed Go files to format-check.');
} else {
  const result = spawnSync('gofmt', ['-l', ...files], { encoding: 'utf8', windowsHide: true });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    process.stderr.write(result.stderr);
    process.exit(result.status || 1);
  }
  if (result.stdout.trim()) {
    console.error('Run gofmt -w on these changed Go files:');
    process.stderr.write(result.stdout);
    process.exit(1);
  }
  console.log(`Formatting passed for ${files.length} changed Go files.`);
}
