#!/usr/bin/env python3
"""Authorize an PaNasMs Cloud Sync account on a computer with a browser."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('provider', choices=['drive','dropbox'])
parser.add_argument('--output', type=Path)
args = parser.parse_args()
if not shutil.which('rclone'):
    sys.exit('Install rclone from https://rclone.org/downloads/ and run this helper again.')
print('Your browser will open. Sign in to the account you want to connect to PaNasMs.')
print('If you need another account, switch accounts in the provider login screen.')
result = subprocess.run(['rclone','authorize',args.provider],stdout=subprocess.PIPE,text=True)
if result.returncode:
    sys.exit('Authorization did not complete.')
token = None
decoder = json.JSONDecoder()
for offset, char in enumerate(result.stdout):
    if char != '{':
        continue
    try:
        value,_ = decoder.raw_decode(result.stdout[offset:])
    except json.JSONDecodeError:
        continue
    if isinstance(value,dict) and value.get('access_token') and value.get('refresh_token'):
        token = value
if token is None:
    sys.exit('No offline authorization was received. Try authorizing again.')
output = args.output or Path('panasms-' + args.provider + '-account.json')
try:
    fd = os.open(output,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
except FileExistsError:
    sys.exit('Output already exists. Choose another --output filename.')
with os.fdopen(fd,'w') as stream:
    json.dump({'format':1,'provider':args.provider,'token':token},stream)
print('Authorization saved to:',output.resolve())
print('Import this private file in Cloud Sync → Add connection. Do not share it or commit it to Git.')
