#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -r $1 ]]; then
  echo "Uso: bash restore-host.sh /caminho/para/chave-de-restauracao.txt" >&2
  exit 2
fi

key=$1
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
archive=$repo_dir/.restore-download/host-config.tar.zst.gpg
project_archive=$repo_dir/.restore-download/private-project.tar.zst.gpg
private_dir=$repo_dir/.restored-private
target=/mnt/sherlock-ssd/chatbot-local/sherlock-audit-lab

[[ -r $archive && -r $project_archive ]] || { echo "Execute restore.sh primeiro." >&2; exit 1; }
[[ -d /mnt/sherlock-ssd ]] || { echo "Monte o SSD em /mnt/sherlock-ssd." >&2; exit 1; }
[[ ! -e $target ]] || { echo "O diretório de integração já existe; nada foi sobrescrito." >&2; exit 1; }

original_user=$(gpg --batch --yes --pinentry-mode loopback --passphrase-file "$key" --decrypt "$archive" |
  zstd -d -c | tar -tf - | awk -F/ '$1 == "home" && $3 == ".config" && $4 == "sherlock-chat" { user=$2 } END { print user }')
[[ -n $original_user ]] || { echo "Não foi possível identificar o usuário original no backup." >&2; exit 1; }
[[ $(id -un) == "$original_user" ]] || { echo "A configuração original exige o mesmo nome de usuário Linux da origem: $original_user" >&2; exit 1; }
[[ ! -e /home/$original_user/.config/sherlock-chat ]] || { echo "Configuração Sherlock já existe; nada foi sobrescrito." >&2; exit 1; }

mkdir -p "$private_dir"
gpg --batch --yes --pinentry-mode loopback --passphrase-file "$key" --decrypt "$project_archive" |
  zstd -d -c | tar -C "$private_dir" -xf -
[[ -d $private_dir/host/sherlock ]] || { echo "Código de integração ausente do pacote privado." >&2; exit 1; }
cp -a "$private_dir/host/sherlock" "$target"
gpg --batch --yes --pinentry-mode loopback --passphrase-file "$key" --decrypt "$archive" |
  zstd -d -c |
  sudo tar -C / -xf -

systemctl --user daemon-reload
echo "Arquivos do host restaurados. Entre na conta Mullvad antes de iniciar o proxy e os serviços."
