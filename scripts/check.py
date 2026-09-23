import json, pathlib, py_compile
root = pathlib.Path(__file__).resolve().parents[1]
locale = root / 'frontend/locales'
en = json.loads((locale / 'en.json').read_text())
for lang in ('ru','uk'):
 assert json.loads((locale / (lang+'.json')).read_text()).keys() == en.keys()
print('Translation keys checked')
