#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -r $1 ]]; then
  echo "Uso: bash restore.sh /caminho/para/chave-de-restauracao.txt" >&2
  exit 2
fi

key=$1
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_name=glaudsonvilela/artex-v3
release_tag=vm-2026-11-23
target_dir=/mnt/sherlock-ssd/Sherlock-VM
download_dir=$repo_dir/.restore-download

for tool in gh gpg zstd sha256sum virsh qemu-img; do
  command -v "$tool" >/dev/null || { echo "Falta: $tool" >&2; exit 1; }
done
mountpoint -q /mnt/sherlock-ssd || { echo "Monte o volume em /mnt/sherlock-ssd antes da restauração." >&2; exit 1; }

if virsh -c qemu:///system dominfo sherlock-v62 >/dev/null 2>&1; then
  echo "A VM sherlock-v62 já existe neste host; nada foi sobrescrito." >&2
  exit 1
fi
if [[ -e $target_dir/sherlock-v62.qcow2 || -e $target_dir/debian-13.7.0-amd64-DVD-1.iso ]]; then
  echo "Já existe disco ou ISO no caminho original; nada foi sobrescrito." >&2
  exit 1
fi
if virsh -c qemu:///system net-info sherlock-web >/dev/null 2>&1 ||
   virsh -c qemu:///system nwfilter-dumpxml sherlock-web-egress >/dev/null 2>&1; then
  echo "A rede ou o filtro originais já existem; nada foi sobrescrito." >&2
  exit 1
fi

mkdir -p "$download_dir"
gh release download "$release_tag" --repo "$repo_name" --dir "$download_dir" --clobber
(cd "$download_dir" && sha256sum -c "$repo_dir/SHA256SUMS")
gpg --batch --yes --pinentry-mode loopback --passphrase-file "$key" --decrypt "$download_dir/private-project.tar.zst.gpg" |
  zstd -d -c | tar -tf - >/dev/null

mkdir -p "$target_dir"
cat "$download_dir"/debian-13.7.0-amd64-DVD-1.iso.chunk-* > "$target_dir/debian-13.7.0-amd64-DVD-1.iso"
cat "$download_dir"/sherlock-v62.qcow2.zst.gpg.chunk-* |
  gpg --batch --yes --pinentry-mode loopback --passphrase-file "$key" --decrypt |
  zstd -d -c > "$target_dir/sherlock-v62.qcow2"
(cd "$target_dir" && sha256sum -c "$repo_dir/vm/IMAGE_SHA256")
qemu-img check "$target_dir/sherlock-v62.qcow2"

virsh -c qemu:///system nwfilter-define "$repo_dir/vm/libvirt/sherlock-web-egress.xml"
virsh -c qemu:///system net-define "$repo_dir/vm/libvirt/sherlock-web.xml"
virsh -c qemu:///system net-start sherlock-web
virsh -c qemu:///system net-autostart sherlock-web
virsh -c qemu:///system define "$repo_dir/vm/libvirt/sherlock-v62.xml"
virsh -c qemu:///system autostart sherlock-v62
echo "VM restaurada. Para iniciar: virsh -c qemu:///system start sherlock-v62"
