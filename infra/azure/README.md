# Azure Container Apps Notes

This repo can run in the same Azure Container Apps environment as `LicitAPI`.

Recommended container app names:

- `nats` - internal TCP ingress, target port `4222`, min replicas `1`
- `bidding-engine` - internal HTTP ingress, target port `5160`
- `auction-streamer` - internal HTTP ingress, target port `5161`
- `payment-validator` - internal HTTP ingress, target port `5162`
- `go-gateway` - optional HTTP ingress, target port `5100`

## Recommended topology

- Use the .NET `gateway` app from `LicitAPI` as the only public gateway.
- Deploy `go-gateway` only if you want to demo the Go gateway separately.

## Required env vars for all Go apps

- `CONFIG_PATH=config.azure.yaml`
- `LICIT_GO_NATS_URL=nats://nats:4222`
- `LICIT_GO_REDIS_CONNECTION_STRING` -> Key Vault secret `azure-redis-connection-string`
- `LICIT_GO_JWT_SECRET` -> Key Vault secret `jwt-secret`

## Required env vars for `bidding-engine`

- `LICIT_GO_BIDDING_DB_CONNECTION_STRING` -> Key Vault secret `bidding-db-connection-string`

## Required env vars for `payment-validator`

- `LICIT_GO_WALLET_SERVICE_URL=http://wallet-service`
- `LICIT_GO_TENDERING_SERVICE_URL=http://tendering-service`
- `LICIT_GO_AUTH_SERVICE_URL=http://auth-service`

## Optional env vars for `go-gateway`

- `LICIT_GO_GATEWAY_ALLOWED_ORIGINS=https://your-frontend-host`

## Notes

- `config.azure.yaml` uses Azure-friendly hostnames and no frontend catch-all route.
- Redis access in Azure uses TLS when the connection string contains `ssl=True`.
- You need a separate PostgreSQL database for the Go bidding service: `LicitBiddingDb`.
- The bidding service migrates its own database schema on startup.
