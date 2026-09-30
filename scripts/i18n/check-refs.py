#!/usr/bin/env python3
"""check-refs.py — every t('ns.key') / tr('…') / i18nT('…') reference in src/
must exist in en.ts. tsc and check-i18n.mjs cannot see this class of bug."""
import os, re, sys
sys.path.insert(0, '/home/user/tools')
import importlib.util
_HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location('m', os.path.join(_HERE, 'i18n-merge.py'))
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)

# Project root: the repo this script lives in (scripts/i18n/…), overridable.
ROOT = os.environ.get('SATURN_ROOT') or os.path.abspath(os.path.join(_HERE, '..', '..'))
text = open(os.path.join(ROOT, 'src/i18n/locales/en.ts'), encoding='utf8').read()
start, end, spans = m.ns_spans(text)
known = set()
for ns, (o, c) in spans.items():
    body = text[o:c]
    for leaf in re.findall(r'^\s{4}([A-Za-z0-9_]+):', body, re.M):
        known.add(f'{ns}.{leaf}')
    for nested in re.finditer(r'^\s{4}([A-Za-z0-9_]+):\s*\{', body, re.M):
        known.add(f'{ns}.{nested.group(1)}')

refs = {}
pat = re.compile(r"\b(?:t|tr|i18nT)\(\s*['\"]([A-Za-z0-9_.]+)['\"]")
for root, dirs, files in os.walk(os.path.join(ROOT, 'src')):
    if 'admin' in root:
        continue
    for fn in files:
        if not fn.endswith(('.ts', '.tsx', '.astro')):
            continue
        p = os.path.join(root, fn)
        src = open(p, encoding='utf8', errors='replace').read()
        # Blank out comments so JSDoc usage examples never count as real refs.
        src = re.sub(r'/\*[\s\S]*?\*/', '', src)
        src = re.sub(r'(?m)^\s*//.*$', '', src)
        for i, line in enumerate(src.split('\n'), 1):
            for mt in pat.finditer(line):
                if '.' not in mt.group(1):
                    continue  # bare values in tests, never a key path
                refs.setdefault(mt.group(1), []).append(f'{os.path.relpath(p, ROOT)}:{i}')

missing = {k: v for k, v in refs.items() if k not in known}
print(f'refs checked: {len(refs)}   missing: {len(missing)}')
for k, v in sorted(missing.items()):
    print(f'  MISSING {k}  ({v[0]})')
