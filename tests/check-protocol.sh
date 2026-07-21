#!/usr/bin/env bash
set -euo pipefail
node - <<'EOF'
const fs = require('fs');
for (const p of ['protocol/v1.schema.json', 'protocol/errors.json', 'protocol/capabilities.json',
                 'protocol/golden/valid-session-open.json', 'protocol/golden/valid-badge.json', 'protocol/golden/corpus.json',
                 'protocol/golden/invalid-mutation-extra-field.json']) JSON.parse(fs.readFileSync(p, 'utf8'));
const invalid = JSON.parse(fs.readFileSync('protocol/golden/invalid-mutation-extra-field.json', 'utf8'));
if (!('forbidden' in invalid.params)) throw new Error('invalid corpus fixture lost its unknown field');
const corpus = JSON.parse(fs.readFileSync('protocol/golden/corpus.json', 'utf8'));
if (corpus.filter(x => x.valid).length !== 12 || corpus.filter(x => !x.valid).length < 6) throw new Error('Slice 0/1 corpus coverage changed');
EOF
