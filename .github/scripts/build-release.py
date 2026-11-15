#!/usr/bin/env python3
import argparse
import datetime
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import zipfile


def run(*args, cwd=None):
    subprocess.run(args, cwd=cwd, check=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    source = args.source.resolve()
    out = args.output.resolve()
    policy = json.loads(Path(__file__).with_name('release-policy.json').read_text())
    version = args.tag.removeprefix('v')
    epoch = int(subprocess.check_output(['git', 'show', '-s', '--format=%ct', 'HEAD'], cwd=source))
    out.mkdir(parents=True, exist_ok=True)
    assets = out / 'assets'
    assets.mkdir()
    binaries = json.loads((source / 'dist/artifacts.json').read_text())
    extra_files = {}
    for pattern in policy.get('archive_files', []):
        for path in sorted(source.glob(pattern)):
            if path.is_file():
                if not path.resolve().is_relative_to(source):
                    raise ValueError('Archive metadata is outside the source directory')
                extra_files[path.name] = path.read_bytes()
    for item in policy['binaries']:
        name = item['asset'].replace('{version}', version)
        matches = [b for b in binaries if b['type'] == 'Binary' and b.get('goos') == item['os'] and b.get('goarch') == item['arch'] and b.get('id', b.get('extra', {}).get('ID')) == item['id']]
        if len(matches) != 1:
            raise ValueError(f'Expected one binary for {item}')
        binary = source / matches[0]['path']
        if not binary.is_file() or not binary.resolve().is_relative_to(source / 'dist'):
            raise ValueError('Invalid build artifact path')
        if name.endswith('.tar.gz'):
            buffer = io.BytesIO()
            with tarfile.open(fileobj=buffer, mode='w') as archive:
                info = tarfile.TarInfo(binary.name)
                info.size = binary.stat().st_size
                info.mode = 0o755
                info.mtime = epoch
                with binary.open('rb') as handle:
                    archive.addfile(info, handle)
                for filename, data in extra_files.items():
                    info = tarfile.TarInfo(filename)
                    info.size = len(data); info.mode = 0o644; info.mtime = epoch
                    archive.addfile(info, io.BytesIO(data))
            with (assets / name).open('wb') as handle:
                with gzip.GzipFile(filename='', mode='wb', fileobj=handle, mtime=epoch) as compressor:
                    compressor.write(buffer.getvalue())
        elif name.endswith('.zip'):
            with zipfile.ZipFile(assets / name, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
                for filename, data in [(binary.name, binary.read_bytes()), *extra_files.items()]:
                    info = zipfile.ZipInfo(filename, (1980, 1, 1, 0, 0, 0))
                    info.external_attr = (0o100755 if filename == binary.name else 0o100644) << 16
                    archive.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED)
        else:
            shutil.copyfile(binary, assets / name)
    checksum = '\n'.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}' for p in sorted(assets.iterdir()))
    (assets / 'checksums.txt').write_text(checksum + '\n')
    for item in policy.get('containers', []):
        context = out / ('context-' + item['id'])
        for arch in ['amd64', 'arm64']:
            matches = [b for b in binaries if b['type'] == 'Binary' and b.get('goos') == 'linux' and b.get('goarch') == arch and b.get('id', b.get('extra', {}).get('ID')) == item['id']]
            if len(matches) != 1:
                raise ValueError('Missing container binary')
            binary = source / matches[0]['path']
            target = context / 'linux' / arch / binary.name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(binary, target)
            target.chmod(0o755)
        images = out / 'images'
        images.mkdir(exist_ok=True)
        labels = dict(item.get('labels', {}), **{
            'org.opencontainers.image.title': item['image'].rsplit('/', 1)[1],
            'org.opencontainers.image.revision': os.environ['TARGET_SHA'],
            'org.opencontainers.image.version': version,
            'org.opencontainers.image.source': 'https://github.com/' + os.environ['GITHUB_REPOSITORY'],
            'org.opencontainers.image.created': datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).isoformat().replace('+00:00', 'Z'),
        })
        label_args = [argument for key, value in labels.items() for argument in ['--label', key + '=' + value]]
        run('docker', 'buildx', 'build', '--platform', 'linux/amd64,linux/arm64', '--provenance=false', '--sbom=false', '--build-arg', f'SOURCE_DATE_EPOCH={epoch}', *label_args, '--output', 'type=oci,rewrite-timestamp=true,dest=' + str(images / item['archive']), '--file', str(source / item['dockerfile']), str(context))
        shutil.rmtree(context)


if __name__ == '__main__':
    main()
