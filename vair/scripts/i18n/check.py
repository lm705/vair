# -*- coding: utf-8 -*-
"""Every string the UI asks for, against every string the dictionary has.

Run from dev/:  python scripts/i18n/check.py

Exits non-zero when a string reaches the interface with no Russian for it. That
is the whole point: a missing translation is invisible in review — the English
shows up and looks deliberate — and it is only ever noticed by the person using
the program in Russian.
"""
import io, re, glob, sys

# Say what we found even where the console is cp1251, which is the console this
# is run on: one arrow inside a hint would otherwise end the run in a traceback
# and report nothing at all.
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except AttributeError:
    pass

# Every way a literal reaches the dictionary, not only the obvious one:
#   tt('…') / t10(lang, '…')  — the direct call
#   hint('…')                 — the settings helper, which is tt inside a <div>
#   needs="…"                 — a label handed to Dependent, translated there
# Only the first was matched for a long time, so a hint written through the
# helper — which is most of the hints — was never checked at all.
#
# Both quote styles, because both are written: a string containing an apostrophe
# is put in double quotes instead of escaping it.
#
# The trailing comma is not optional cosmetics: prettier puts one after any
# argument it had to wrap onto its own line, which is every string long enough
# to be worth translating. Without it in the pattern the checker quietly skipped
# exactly the strings most likely to be missing a translation.
KEY = re.compile(
    r"(?:\btt|\bt10|\bhint)\(\s*(?:lang(?:Ref\.current)?\s*,\s*)?"
    r"(?:'((?:[^'\\]|\\.)*)'|\"((?:[^\"\\]|\\.)*)\")\s*,?\s*\)"
)
NEEDS = re.compile(r"\bneeds=(?:\"([^\"]*)\"|'([^']*)')")
# A description handed to <Hint> as a prop rather than called: `en="…"`,
# `en={'…'}` and the same as an object property. It is named `en` because it
# holds the English source — the key — and is translated where it is drawn.
STR = r"(?:'((?:[^'\\]|\\.)*)'|\"((?:[^\"\\]|\\.)*)\")"
PROP = re.compile(r"\ben\s*[:=]\s*\{?\s*" + STR)
# One choice of a multiple-choice setting: opt('name', 'what it does'). Both
# halves are shown, so both need Russian. Its sibling ex('germany+ws+tls', '…')
# is a worked example, and only the right half is prose — the left one is what
# the user types, and asking for a Russian translation of it would be a bug in
# the checker rather than in the interface.
OPT = re.compile(r"\bopt\(\s*" + STR + r"\s*,\s*" + STR + r"\s*,?\s*\)", re.S)
EX = re.compile(r"\bex\(\s*(?:'(?:[^'\\]|\\.)*'|\"(?:[^\"\\]|\\.)*\")\s*,\s*" + STR + r"\s*,?\s*\)", re.S)
DICT = re.compile(r'\n    "((?:[^"\\]|\\.)*)"\s*:')

def unescape(k):
    # \' and \" in the source are just the character at runtime.
    return k.replace(chr(92) + chr(39), chr(39)).replace(chr(92) + chr(34), chr(34))


used = {}
for f in sorted(glob.glob("frontend/src/*.tsx") + glob.glob("frontend/src/*.ts")):
    if f.endswith("i18n.ts"):
        continue
    s = io.open(f, encoding="utf-8").read()
    for pat in (KEY, NEEDS, PROP, OPT, EX):
        for m in pat.finditer(s):
            # Each literal is one quoted group of the pair the pattern offers;
            # opt() offers two pairs, and both are keys.
            gs = m.groups()
            for a, b in zip(gs[0::2], gs[1::2]):
                k = a if a is not None else b
                if k is not None:
                    used.setdefault(unescape(k), set()).add(f.replace("\\", "/").split("/")[-1])

raw = DICT.findall(io.open("frontend/src/i18n.ts", encoding="utf-8").read())
have = {k.replace(chr(92) + chr(34), chr(34)).replace(chr(92) + chr(39), chr(39)) for k in raw}
# "Sources" is deliberately the same word in both languages.
have.add("Sources")
missing = sorted(k for k in used if k not in have)

print("used %d, in dictionary %d, missing %d" % (len(used), len(have), len(missing)))
for m in missing:
    print("  [%s] %s" % (",".join(sorted(used[m])), m))

raise SystemExit(1 if missing else 0)
