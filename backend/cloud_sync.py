import configparser
import ctypes
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import select
import signal
import sqlite3
import socketserver
import threading
import struct
import subprocess
import sys
import time
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(sys.argv[2]) if len(sys.argv) > 2 else Path('/tmp/cloud-sync-test')
STOP = False


class Error(Exception):
    pass


def check(value, message):
    if not value:
        raise Error(message)


def connect():
    ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    db = sqlite3.connect(ROOT / 'state.db', timeout=15)
    db.row_factory = sqlite3.Row
    db.execute('PRAGMA journal_mode=WAL')
    db.execute('PRAGMA synchronous=FULL')
    db.execute('PRAGMA foreign_keys=ON')
    db.executescript('''
    CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,provider TEXT NOT NULL,label TEXT NOT NULL,identity TEXT NOT NULL,cursor TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '');
    CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,account TEXT NOT NULL REFERENCES accounts(id),name TEXT NOT NULL,local TEXT NOT NULL,remote TEXT NOT NULL,direction TEXT NOT NULL,mount TEXT NOT NULL,paused INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT 'queued',dirty INTEGER NOT NULL DEFAULT 1,initialized INTEGER NOT NULL DEFAULT 0,snapshot TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',last_sync INTEGER NOT NULL DEFAULT 0);
    CREATE TABLE IF NOT EXISTS history(id INTEGER PRIMARY KEY,task TEXT,at INTEGER NOT NULL,kind TEXT NOT NULL,message TEXT NOT NULL);
    ''')
    return db


def event(db, task, kind, message):
    db.execute('INSERT INTO history(task,at,kind,message) VALUES(?,?,?,?)', (task, int(time.time()), kind, message))
    db.execute('DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT 500)')


def config_path(account):
    check(re.fullmatch('[a-f0-9]{32}', account), 'Invalid account')
    return ROOT / (account + '.conf')


def rclone(account, arguments, timeout=120, cancelled=None):
    args = ['rclone', '--config', str(config_path(account)), '--cache-dir', str(ROOT / 'cache'),
            '--contimeout', '15s', '--timeout', '60s', '--retries', '1', '--low-level-retries', '2',
            '--drive-skip-gdocs', '--drive-skip-shortcuts', *arguments]
    # Logs may contain provider details; never return raw subprocess output to the UI.
    with tempfile.TemporaryFile(dir=ROOT) as output, tempfile.TemporaryFile(dir=ROOT) as errors:
        proc = subprocess.Popen(args, stdout=output, stderr=errors, start_new_session=True)
        start = time.monotonic()
        while proc.poll() is None:
            if STOP or errors.tell() > 4 * 1024 * 1024 or output.tell() > 32 * 1024 * 1024 or time.monotonic() - start > timeout or (cancelled and cancelled()):
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()
                raise Error('Transfer stopped; review the task before resuming.')
            time.sleep(0.2)
        if proc.returncode:
            errors.seek(0)
            reason = errors.read(4 * 1024 * 1024).decode(errors='replace').lower()
            if 'all files were changed' in reason or 'max-delete' in reason or 'too many deletes' in reason:
                raise Error('Safety check stopped the task: too many files changed or were deleted. Review both folders before retrying.')
            if 'resync' in reason and 'bisync' in arguments:
                raise Error('Two-way sync history requires recovery. Preserve both folders and create a new task with an empty destination; automatic reset is disabled.')
        check(proc.returncode == 0, 'Cloud operation failed. Check account access, folder availability and quota; reconnect if necessary.')
        check(output.tell() <= 32 * 1024 * 1024, 'Folder listing exceeds prototype limit')
        output.seek(0)
        return output.read().decode()


def token(account):
    config = configparser.RawConfigParser()
    config.read(config_path(account))
    value = json.loads(config['cloud']['token'])
    # Let rclone refresh its own OAuth client credentials; tokens never enter argv.
    import datetime
    try:
        expiry = datetime.datetime.fromisoformat(value.get('expiry', '').replace('Z', '+00:00')).timestamp()
    except ValueError:
        expiry = 0
    if expiry < time.time() + 120:
        rclone(account, ['lsd', 'cloud:', '--max-depth', '1'])
        config.read(config_path(account))
        value = json.loads(config['cloud']['token'])
    return value['access_token']


def api(account, url, body=None):
    data = json.dumps(body).encode() if body is not None or url.startswith('https://api.dropboxapi.com/') else None
    request = urllib.request.Request(url, data=data, headers={'Authorization': 'Bearer ' + token(account), 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=25) as response:
            raw = response.read(4 * 1024 * 1024 + 1)
        check(len(raw) <= 4 * 1024 * 1024, 'Cloud response is too large')
        return json.loads(raw)
    except urllib.error.HTTPError as error:
        if error.code in (401, 403):
            raise Error('Account authorization expired or access was denied. Reconnect the account.')
        if error.code in (409, 410):
            raise Error('Cloud change cursor expired. Reconnect the account to rebuild it.')
        if error.code == 429:
            raise Error('Cloud rate limit reached. Checks will resume with backoff.')
        raise Error('Cloud request failed; retry later.')
    except (urllib.error.URLError, TimeoutError):
        raise Error('Cloud is unreachable; retry later.')


def initial_cursor(account, provider):
    if provider == 'drive':
        return api(account, 'https://www.googleapis.com/drive/v3/changes/startPageToken')['startPageToken']
    return api(account, 'https://api.dropboxapi.com/2/files/list_folder/get_latest_cursor', {'path': '', 'recursive': True, 'include_deleted': True})['cursor']


def poll(account):
    cursor, changed = account['cursor'], False
    if not cursor:
        return initial_cursor(account['id'], account['provider']), True
    for _ in range(100):
        if account['provider'] == 'drive':
            query = urllib.parse.urlencode({'pageToken': cursor, 'pageSize': 1000, 'fields': 'nextPageToken,newStartPageToken,changes(fileId,removed)'})
            data = api(account['id'], 'https://www.googleapis.com/drive/v3/changes?' + query)
            changed = changed or bool(data.get('changes'))
            cursor = data.get('nextPageToken') or data['newStartPageToken']
            more = bool(data.get('nextPageToken'))
        else:
            data = api(account['id'], 'https://api.dropboxapi.com/2/files/list_folder/continue', {'cursor': cursor})
            changed = changed or bool(data.get('entries'))
            cursor, more = data['cursor'], data.get('has_more', False)
        if not more:
            return cursor, changed
    raise Error('Too many cloud changes; manual reconciliation is required.')


def mount_identity(path):
    candidates = []
    for line in Path('/proc/self/mountinfo').read_text().splitlines():
        left, right = line.split(' - ', 1)
        fields, tail = left.split(), right.split()
        target = re.sub(r'\\([0-7]{3})', lambda m: chr(int(m[1], 8)), fields[4])
        if path == target or path.startswith(target.rstrip('/') + '/'):
            candidates.append((len(target), fields[2], fields[3], target, tail[0], tail[1]))
    check(candidates, 'Local volume is unavailable')
    value = max(candidates)
    check(value[4] in ('ext4', 'xfs', 'btrfs', 'ext3', 'ext2'), 'Use a local Linux filesystem; network mounts and removable FAT/NTFS are not supported in this prototype.')
    return json.dumps(value[1:])


def local_folder(value):
    check(isinstance(value, str) and os.path.isabs(value), 'Select an absolute local folder path')
    path = Path(value)
    check(str(path.resolve()) == str(path), 'Symlink paths are not supported')
    check(any(str(path).startswith(prefix) for prefix in ('/home/', '/srv/', '/mnt/', '/media/')), 'Choose a folder inside /home, /srv, /mnt or /media')
    check(path.is_dir() and os.access(path, os.R_OK | os.W_OK | os.X_OK), 'Local folder must exist and be writable by your Linux user')
    return str(path), mount_identity(str(path))


def remote_folder(value):
    check(isinstance(value, str) and len(value) <= 2048 and not any(ord(c) < 32 for c in value), 'Invalid cloud folder')
    value = value.strip('/')
    check(all(p not in ('.', '..') for p in value.split('/')), 'Invalid cloud folder')
    return value


def listing(account, remote):
    raw = rclone(account, ['lsjson', 'cloud:' + remote, '-R', '--hash'])
    entries = json.loads(raw)
    check(len(entries) <= 100000, 'Prototype supports up to 100000 entries per task')
    check(len({e['Path'] for e in entries}) == len(entries), 'Duplicate cloud filenames must be resolved first')
    normalized = sorted([(e['Path'], e['Size'], e.get('ModTime'), e.get('IsDir'), e.get('Hashes', {})) for e in entries], key=lambda e: e[0])
    return hashlib.sha256(json.dumps(normalized, sort_keys=True).encode()).hexdigest(), entries


def account_import(db, p):
    provider = p.get('provider')
    check(provider in ('drive', 'dropbox'), 'Unsupported provider')
    value = p.get('authorization', {})
    check(isinstance(value, dict) and value.get('provider') == provider, 'Authorization file belongs to another provider')
    secret = value.get('token', {})
    check(isinstance(secret, dict) and isinstance(secret.get('access_token'), str) and isinstance(secret.get('refresh_token'), str), 'Authorization file must include access and refresh tokens')
    account_id = p.get('id') or uuid.uuid4().hex
    previous = db.execute('SELECT * FROM accounts WHERE id=?', (account_id,)).fetchone()
    if p.get('id'):
        check(previous and previous['provider'] == provider, 'Account not found')
        check(not db.execute("SELECT 1 FROM tasks WHERE account=? AND status='running'", (account_id,)).fetchone(), 'Pause running tasks first')
    config = config_path(account_id)
    old = config.read_bytes() if config.exists() else None
    content = '[cloud]\ntype = ' + provider + '\ntoken = ' + json.dumps(secret, separators=(',', ':')) + '\n'
    for name in ('client_id', 'client_secret'):
        if value.get(name):
            check(isinstance(value[name], str) and '\n' not in value[name] and '\r' not in value[name], 'Invalid OAuth client setting')
            content += name + ' = ' + value[name] + '\n'
    config.write_text(content)
    config.chmod(0o600)
    try:
        if provider == 'drive':
            data = api(account_id, 'https://www.googleapis.com/drive/v3/about?fields=user')
            who = data['user']['permissionId']
        else:
            data = api(account_id, 'https://api.dropboxapi.com/2/users/get_current_account', None)
            who = data['account_id']
        check(not previous or previous['identity'] == who, 'Reconnect using the same cloud account')
        cursor = initial_cursor(account_id, provider)
        label = str(p.get('label', '')).strip()[:100] or who
        with db:
            db.execute('INSERT INTO accounts(id,provider,label,identity,cursor) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET label=excluded.label,cursor=excluded.cursor,error=\'\'', (account_id, provider, label, who, cursor))
            db.execute("UPDATE tasks SET dirty=1,error='',status='queued' WHERE account=? AND status!='running'", (account_id,))
        return {'id': account_id}
    except Exception:
        if old is None:
            config.unlink(missing_ok=True)
        else:
            config.write_bytes(old)
        raise


def action(db, p):
    kind = p.get('action')
    if kind == 'account.save':
        return account_import(db, p)
    if kind == 'folders':
        check(db.execute('SELECT 1 FROM accounts WHERE id=?', (p.get('account'),)).fetchone(), 'Account not found')
        path = remote_folder(p.get('path', ''))
        entries = json.loads(rclone(p['account'], ['lsjson', 'cloud:' + path, '--dirs-only']))
        return {'folders': [{'name': e['Name'], 'path': '/'.join(filter(None, [path, e['Name']]))} for e in entries]}
    if kind == 'account.remove':
        check(not db.execute('SELECT 1 FROM tasks WHERE account=?', (p.get('id'),)).fetchone(), 'Remove tasks before disconnecting this account')
        with db:
            db.execute('DELETE FROM accounts WHERE id=?', (p.get('id'),))
        config_path(p.get('id')).unlink(missing_ok=True)
        return {}
    if kind == 'task.create':
        check(db.execute('SELECT 1 FROM accounts WHERE id=?', (p.get('account'),)).fetchone(), 'Account not found')
        local, mount = local_folder(p.get('local'))
        remote = remote_folder(p.get('remote', ''))
        check(p.get('direction') in ('upload', 'download', 'both'), 'Invalid sync direction')
        for task in db.execute('SELECT * FROM tasks'):
            check(not (local == task['local'] or local.startswith(task['local'] + '/') or task['local'].startswith(local + '/')), 'Local sync folders must not overlap')
            if task['account'] == p['account']:
                check(remote and task['remote'] and not (remote == task['remote'] or remote.startswith(task['remote'] + '/') or task['remote'].startswith(remote + '/')), 'Cloud sync folders must not overlap')
        check(len(db.execute('SELECT id FROM tasks').fetchall()) < 32, 'Prototype supports up to 32 tasks per user')
        listing(p['account'], remote)
        task_id = uuid.uuid4().hex
        with db:
            db.execute('INSERT INTO tasks(id,account,name,local,remote,direction,mount) VALUES(?,?,?,?,?,?,?)', (task_id,p['account'],str(p.get('name','')).strip()[:100] or Path(local).name,local,remote,p['direction'],mount))
            event(db,task_id,'created','Task created')
        return {'id':task_id}
    check(kind in ('task.pause','task.resume','task.run','task.remove'), 'Unknown action')
    db.execute('BEGIN IMMEDIATE')
    task = db.execute('SELECT * FROM tasks WHERE id=?', (p.get('id'),)).fetchone()
    check(task, 'Task not found')
    with db:
        if kind == 'task.pause':
            db.execute('UPDATE tasks SET paused=1 WHERE id=?',(task['id'],))
        elif kind == 'task.remove':
            check(task['status'] != 'running', 'Pause the task and wait for the transfer to stop')
            db.execute('DELETE FROM tasks WHERE id=?',(task['id'],))
            event(db,task['id'],'removed','Task removed; local and cloud files preserved')
        else:
            check(task['status'] != 'running', 'Task is already running')
            db.execute("UPDATE tasks SET paused=0,dirty=1,status='queued',error='' WHERE id=?",(task['id'],))
    return {}


class Watcher:
    def __init__(self):
        self.lib = ctypes.CDLL(None, use_errno=True)
        self.fd = self.lib.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
        check(self.fd >= 0, 'Cannot monitor local files')
        self.paths = {}

    def add_tree(self, path, task):
        count = 0
        for folder, dirs, _ in os.walk(path, followlinks=False):
            dirs[:] = [d for d in dirs if not os.path.islink(os.path.join(folder,d)) and not d.startswith('.panasms-cloud-')]
            wd = self.lib.inotify_add_watch(self.fd, os.fsencode(folder), 0x2 | 0x4 | 0x8 | 0x40 | 0x80 | 0x100 | 0x200 | 0x400 | 0x800 | 0x2000)
            check(wd >= 0, 'Local watch limit reached; increase inotify limits before resuming')
            self.paths[wd] = task
            count += 1
            check(count <= 100000, 'Prototype directory watch limit reached')

    def drain(self):
        affected, rebuild = set(), False
        while True:
            try:
                data = os.read(self.fd, 1024*1024)
            except BlockingIOError:
                break
            pos = 0
            while pos < len(data):
                wd, mask, _, size = struct.unpack_from('iIII',data,pos)
                pos += 16 + size
                if mask & 0x4000:
                    affected.update(self.paths.values()); rebuild = True
                elif wd in self.paths:
                    affected.add(self.paths[wd])
                    rebuild |= bool(mask & (0x100|0x200|0x40|0x80|0x400|0x800|0x2000))
        return affected, rebuild

    def close(self):
        os.close(self.fd)


def run_task(db, task):
    check(mount_identity(task['local']) == task['mount'], 'Local volume changed or is unavailable; reconnect the original volume')
    check(Path(task['local']).is_dir(), 'Local folder is unavailable')
    snapshot, entries = listing(task['account'],task['remote'])
    target = 'cloud:' + task['remote']
    work = ROOT / 'tasks' / task['id']
    work.mkdir(parents=True,exist_ok=True)
    if task['direction'] == 'both':
        if not task['initialized']:
            local_files = any(files for _,_,files in os.walk(task['local']))
            check(not local_files or not any(not e['IsDir'] for e in entries), 'For the first two-way sync, one folder must be empty. Use a new folder to avoid initial conflicts.')
        args = ['bisync',task['local'],target,'--workdir',str(work),'--max-delete','10']
        if not task['initialized']:
            args.append('--resync')
    else:
        source,dest = (task['local'],target) if task['direction']=='upload' else (target,task['local'])
        # One-way prototype uses copy: deletions are never propagated.
        args = ['copy',source,dest,'--create-empty-src-dirs','--backup-dir',
                (target.rstrip('/') + '/.panasms-cloud-versions/' if task['direction']=='upload' else task['local']+'/.panasms-cloud-versions/') + str(int(time.time())) + '-' + uuid.uuid4().hex[:8]]
    args += ['--exclude','.panasms-cloud-versions/**','--exclude','.panasms-cloud-conflicts/**','--transfers','2','--checkers','2']
    def cancelled():
        current = db.execute('SELECT paused FROM tasks WHERE id=?',(task['id'],)).fetchone()
        return not current or current['paused']
    rclone(task['account'],args,timeout=86400,cancelled=cancelled)
    # Keep the pre-transfer baseline so concurrent cloud edits remain visible to the next poll.
    with db:
        db.execute("UPDATE tasks SET status='idle',initialized=1,snapshot=?,last_sync=?,error='' WHERE id=?",(snapshot,int(time.time()),task['id']))
        event(db,task['id'],'success','Synchronization completed')


def worker():
    global STOP
    def stop(*_):
        global STOP
        STOP = True
    signal.signal(signal.SIGTERM,stop)
    signal.signal(signal.SIGINT,stop)
    lock = (ROOT / '.worker.lock').open('w')
    try:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
    except BlockingIOError:
        return
    db = connect()
    socket = ROOT / 'worker.sock'
    socket.unlink(missing_ok=True)
    server = socketserver.ThreadingUnixStreamServer(str(socket), RequestHandler)
    server.daemon_threads = True
    socket.chmod(0o600)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    with db:
        db.execute("UPDATE tasks SET status='error',error='Transfer interrupted; review before resuming.',paused=1 WHERE status='running'")
        db.execute("UPDATE tasks SET dirty=1 WHERE paused=0 AND status!='error'")
    watcher = Watcher()
    watched, next_poll, failures = set(), {}, {}
    while not STOP:
        try:
            for task in db.execute('SELECT * FROM tasks WHERE paused=0').fetchall():
                if task['id'] not in watched:
                    try:
                        check(mount_identity(task['local']) == task['mount'], 'Local volume changed or is unavailable')
                        watcher.add_tree(task['local'],task['id']); watched.add(task['id'])
                    except Error as error:
                        with db:
                            db.execute("UPDATE tasks SET paused=1,status='error',error=? WHERE id=?",(str(error),task['id']))
            watched.intersection_update(row['id'] for row in db.execute('SELECT id FROM tasks WHERE paused=0'))
            affected,_ = watcher.drain()
            if affected:
                with db:
                    db.executemany('UPDATE tasks SET dirty=1 WHERE id=?',[(mid,) for mid in affected])
            now = time.monotonic()
            for account in db.execute('SELECT * FROM accounts').fetchall():
                tasks = db.execute('SELECT * FROM tasks WHERE account=? AND paused=0',(account['id'],)).fetchall()
                if not tasks or next_poll.get(account['id'],0) > now:
                    continue
                next_poll[account['id']] = now + 60
                try:
                    with (ROOT / '.api.lock').open('w') as op_lock:
                        fcntl.flock(op_lock,fcntl.LOCK_EX)
                        cursor,changed = poll(account)
                        dirty = []
                        if changed:
                            for task in tasks:
                                snapshot,_ = listing(account['id'],task['remote'])
                                if snapshot != task['snapshot']:
                                    dirty.append(task['id'])
                    with db:
                        db.execute("UPDATE accounts SET cursor=?,error='' WHERE id=?",(cursor,account['id']))
                        db.executemany('UPDATE tasks SET dirty=1 WHERE id=?',[(mid,) for mid in dirty])
                    failures[account['id']] = 0
                except Exception as error:
                    failures[account['id']] = failures.get(account['id'],0)+1
                    next_poll[account['id']] = now + min(1800,60*2**min(5,failures[account['id']]))
                    with db:
                        db.execute('UPDATE accounts SET error=? WHERE id=?',(str(error) if isinstance(error,Error) else 'Cloud check failed; retrying later.',account['id']))
            task = db.execute("SELECT * FROM tasks WHERE paused=0 AND dirty=1 AND status!='error' ORDER BY last_sync LIMIT 1").fetchone()
            if task:
                with db:
                    claimed = db.execute("UPDATE tasks SET status='running',dirty=0,error='' WHERE id=? AND paused=0 AND dirty=1 AND status!='error'", (task['id'],)).rowcount
                if not claimed:
                    continue
                print(json.dumps({'busy':True}),flush=True)
                try:
                    with (ROOT / '.api.lock').open('w') as op_lock:
                        fcntl.flock(op_lock,fcntl.LOCK_EX)
                        run_task(db,task)
                    watcher.add_tree(task['local'],task['id'])
                except Exception as error:
                    with db:
                        message = str(error) if isinstance(error,Error) else 'Synchronization failed; review before retrying.'
                        db.execute("UPDATE tasks SET status='error',error=? WHERE id=?",(message,task['id']))
                        event(db,task['id'],'error',message)
                finally:
                    print(json.dumps({'busy':False}),flush=True)
            else:
                select.select([watcher.fd],[],[],1)
        except Exception:
            time.sleep(5)
    watcher.close()


def request(mode, p=None):
    db = connect()
    try:
        if mode == 'state':
            return {'accounts':[dict(row) for row in db.execute('SELECT id,provider,label,identity,error FROM accounts')],
                    'tasks':[dict(row) for row in db.execute('SELECT * FROM tasks')],
                    'history':[dict(row) for row in db.execute('SELECT * FROM history ORDER BY id DESC LIMIT 100')]}
        check(mode == 'action' and isinstance(p,dict), 'Invalid request')
        if p.get('action') in ('task.pause','task.resume','task.run','task.remove'):
            return action(db,p)
        with (ROOT / '.api.lock').open('w') as lock:
            try:
                fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
            except BlockingIOError:
                raise Error('A transfer is running. Pause it before changing connections or browsing cloud folders.')
            return action(db,p)
    except Error as error:
        return {'error':str(error)}
    except Exception:
        return {'error':'Cloud Sync operation failed. Check the connection and try again.'}
    finally:
        db.close()


class RequestHandler(socketserver.StreamRequestHandler):
    def handle(self):
        self.connection.settimeout(150)
        raw = self.rfile.readline(128*1024+1)
        if len(raw) > 128*1024:
            return
        try:
            message = json.loads(raw)
            result = request(message.get('mode'), message.get('params'))
        except (ValueError, AttributeError):
            result = {'error':'Invalid request'}
        self.wfile.write(json.dumps(result).encode()+b'\n')


def main():
    os.umask(0o077)
    if sys.argv[1] == 'worker':
        worker()
    else:
        print(json.dumps(request(sys.argv[1], json.load(sys.stdin) if sys.argv[1] == 'action' else None)))


if __name__ == '__main__':
    main()
