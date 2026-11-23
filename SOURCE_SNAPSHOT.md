# Origem dos arquivos de código

| Componente | Origem | Commit base | Estado incluído |
|---|---|---|---|
| ARTEX | https://github.com/BaaRai/ARTEX | `d89cf781b790ec9a1d071bee7e79de489ed05252` | Commit base + alterações locais em `artex/` e `patches/artex-local.patch` |
| Integração Sherlock no host | checkout local | `f738bd35f6add8abe4922d38eaa736d0a3163efa` | Commit base + alterações locais no pacote criptografado `private-project.tar.zst.gpg` |

Os códigos foram copiados dos checkouts ativos. O código ARTEX está em `artex/`. O código de integração no host está no pacote criptografado, pois contém caminhos locais e configurações pessoais. Dependências baixadas, caches, ambientes virtuais e relatórios operacionais ficaram fora da árvore Git. O disco completo da VM, na release criptografada, preserva os binários e dados instalados no Debian.

## Alterações locais incluídas

ARTEX: `agent/chat.go`, `agent/promptcatalog.go`, `agent/auto_execution_test.go`, `llmpool/health.go`, `llmpool/health_order_test.go`, `llmpool/health_policy_test.go`, `llmpool/pool.go`, `llmpool/pool_test.go`.

Sherlock host: `integracoes/artex_bridge.py`, `scripts/artex_feedback.py`, `scripts/sherlock_chat.py`, `tests/test_artex_chat_route.py`, `tests/test_artex_feedback.py`.
