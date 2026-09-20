import importlib.util
from pathlib import Path
import tempfile
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('cloud', Path(__file__).parents[1] / 'backend/cloud_sync.py')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
with tempfile.TemporaryDirectory(prefix='cloud-sync-smoke-') as tmp:
    c.ROOT = Path(tmp) / 'state'
    db = c.connect()
    account = 'a' * 32
    c.config_path(account).write_text('[cloud]\ntype = local\n')
    db.execute('INSERT INTO accounts(id,provider,label,identity) VALUES(?,?,?,?)', (account, 'drive', 'test', 'test'))
    db.commit()
    for direction in ('upload', 'download', 'both'):
        local = Path(tmp) / (direction + '-local')
        remote = Path(tmp) / (direction + '-remote')
        local.mkdir(); remote.mkdir()
        source, dest = (remote, local) if direction == 'download' else (local, remote)
        (source / 'file.txt').write_text('first')
        if direction == 'both':
            for index in range(20):
                (source / f'unchanged-{index}.txt').write_text('unchanged')
        task = dict(id=direction, account=account, local=str(local), remote=str(remote), direction=direction, mount='test', initialized=0)
        db.execute('INSERT INTO tasks(id,account,name,local,remote,direction,mount) VALUES(?,?,?,?,?,?,?)', (direction, account, direction, str(local), str(remote), direction, 'test'))
        db.commit()
        with patch.object(c, 'mount_identity', return_value='test'):
            c.run_task(db, task)
            assert (dest / 'file.txt').read_text() == 'first'
            task['initialized'] = 1
            (source / 'file.txt').write_text('second longer version')
            c.run_task(db, task)
            assert (dest / 'file.txt').read_text() == 'second longer version'
            (source / 'file.txt').unlink()
            if direction != 'both':
                c.run_task(db, task)
                assert (dest / 'file.txt').exists(), 'one-way unexpectedly propagated deletion'
                assert any(p.read_text() == 'first' for p in (dest / '.panasms-cloud-versions').rglob('file.txt'))
        print(direction + ': real rclone transfer verified')
    db.close()
