#!/usr/bin/env python3
"""i18n-merge.py — SAFE replacement for tools/i18n-add.mjs.

The JS tool inserted blocks at a byte offset that could land in the MIDDLE of a
string (it corrupted en/es/de/fr/pt once on add-support-staker.json and again on
add-static.json + add-libl1.json), so this scanner does the insertion properly:

  * a string/comment-aware brace scanner finds the ROOT object and, inside it,
    every `ns: { … }` span (depth 1 → 2);
  * a new key is inserted just before the namespace's closing brace, formatted
    like the surrounding code;
  * a namespace that does not exist yet is appended as a new root-level block;
  * keys that are already present are SKIPPED, so the tool is idempotent and can
    be re-run over a whole directory of add-*.json files safely.

Usage:  python3 tools/i18n-merge.py add-staker2.json [more.json …]
        (paths relative to the repo root, or absolute)
"""
import json
import os
import re
import sys

# Project root: the repo this script lives in (scripts/i18n/…), overridable.
ROOT = os.environ.get('SATURN_ROOT') or os.path.abspath(
    os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', '..'))
LOCALES = os.path.join(ROOT, 'src', 'i18n', 'locales')
LANGS = ['en', 'es', 'de', 'fr', 'pt', 'tr']


def scan(text):
    """Yield (index, char, depth) for every char outside strings/comments."""
    i, n = 0, len(text)
    depth = 0
    while i < n:
        c = text[i]
        if c == '/' and i + 1 < n and text[i + 1] == '/':
            j = text.find('\n', i)
            i = n if j < 0 else j
            continue
        if c == '/' and i + 1 < n and text[i + 1] == '*':
            j = text.find('*/', i + 2)
            i = n if j < 0 else j + 2
            continue
        if c in '\'"`':
            q = c
            i += 1
            while i < n:
                if text[i] == '\\':
                    i += 2
                    continue
                if text[i] == q:
                    i += 1
                    break
                i += 1
            continue
        if c == '{':
            yield i, c, depth
            depth += 1
            i += 1
            continue
        if c == '}':
            depth -= 1
            yield i, c, depth
            i += 1
            continue
        i += 1


def root_span(text):
    """(open_idx, close_idx) of the root object literal."""
    m = re.search(r'=\s*\{', text)
    if not m:
        raise SystemExit('no root object found')
    start = text.index('{', m.start())
    for i, c, d in scan(text):
        if i < start:
            continue
        if c == '}' and d == 0:
            return start, i
    raise SystemExit('unbalanced root object')


def ns_spans(text):
    """(root_open, root_close, {ns: (open_idx, close_idx)}) for depth-1 objects."""
    start, end = root_span(text)
    spans = {}
    stack = []
    for i, c, d in scan(text):
        if i < start or i > end:
            continue
        if c == '{':
            if d == 1:
                m = re.search(r'([A-Za-z0-9_]+)\s*:\s*\{$', text[max(0, i - 200):i + 1])
                stack.append((m.group(1) if m else None, i))
        else:
            if d == 1 and stack:
                name, o = stack.pop()
                if name:
                    spans[name] = (o, i)
    return start, end, spans


def quote(value):
    if "'" in value and '"' not in value:
        return '"' + value + '"'
    return "'" + value.replace('\\', '\\\\').replace("'", "\\'") + "'"


def leaf_source(key, val):
    if isinstance(val, str):
        return f"    {key}: {quote(val)},\n"
    if isinstance(val, dict):
        parts = ', '.join(f"{k}: {quote(v)}" for k, v in val.items())
        return f"    {key}: {{ {parts} }},\n"
    raise SystemExit(f'unsupported value for {key}: {val!r}')


def existing_keys(text, ns_open, ns_close):
    body = text[ns_open:ns_close]
    return set(re.findall(r'^\s{4}([A-Za-z0-9_]+):', body, re.M))


def merge_file(path, additions, report):
    text = open(path, encoding='utf8').read()
    for ns, keys in additions.items():
        want = {k: v for k, v in keys.items()}
        # find the namespace span (re-scan each time: offsets move as we edit)
        start, end, spans = ns_spans(text)
        if ns not in spans:
            block = f"  /* ---- {ns} ---- */\n  {ns}: {{\n" + ''.join(leaf_source(k, v) for k, v in want.items()) + "  },\n"
            text = text[:end] + '\n' + block + text[end:]
            report.append(f'{os.path.basename(path)}: +namespace {ns} ({len(want)} keys)')
            continue
        o, c = spans[ns]
        have = existing_keys(text, o, c)
        missing = {k: v for k, v in want.items() if k not in have}
        skipped = len(want) - len(missing)
        if not missing:
            report.append(f'{os.path.basename(path)}: {ns} up to date ({skipped} keys already there)')
            continue
        # Insert before the namespace's closing brace, matching the indentation
        # the namespace's own leaves already use (robust for flat AND nested ns).
        # The namespace's closing brace sits at the namespace's own depth, so
        # its direct leaves are exactly two spaces deeper. Counting what is
        # already there is NOT reliable: a namespace whose body ends in a large
        # nested object makes the nested indent the most common one, which is
        # how keys ended up indented 6 spaces (wrong, and invisible to
        # tools/check-refs.py's 4-space leaf scan).
        line_start = text.rfind('\n', 0, c) + 1
        brace_indent = re.match(r'\s*', text[line_start:c]).group(0)
        leaf_indent = brace_indent + '  '
        if len(brace_indent) != len(leaf_indent) - 2:
            brace_indent = leaf_indent[:-2] if len(leaf_indent) >= 2 else ''
            text = text[:line_start] + brace_indent + text[c:]
            c = line_start + len(brace_indent)
        insertion = ''.join(line_source(k, v, leaf_indent) for k, v in missing.items())
        text = text[:c] + insertion + text[c:]
        report.append(f'{os.path.basename(path)}: {ns} +{len(missing)} keys' + (f' (skipped {skipped})' if skipped else ''))
    open(path, 'w', encoding='utf8').write(text)


def line_source(key, val, indent):
    """Re-indent a leaf line from leaf_source's 4 spaces to `indent`."""
    body = leaf_source(key, val)
    return ''.join((indent + ln[4:] if ln.startswith('    ') else ln) for ln in body.splitlines(True))


def main(argv):
    if not argv:
        raise SystemExit(__doc__)
    report = []
    for arg in argv:
        path = arg if os.path.isabs(arg) else os.path.join('/home/user/tools', arg)
        data = json.load(open(path, encoding='utf8'))
        for lang in LANGS:
            lp = os.path.join(LOCALES, f'{lang}.ts')
            per_lang = {}
            for ns, keys in data.items():
                vals = {k: (v.get(lang) if isinstance(v, dict) else v) for k, v in keys.items()}
                missing_en = [k for k, v in vals.items() if v is None]
                if missing_en:
                    raise SystemExit(f'{os.path.basename(path)}: {ns} has no "{lang}" value for {missing_en}')
                per_lang[ns] = vals
            merge_file(lp, per_lang, report)
    for line in report:
        print(line)


if __name__ == '__main__':
    main(sys.argv[1:])
