# apis — shared gRPC contracts

Contracts that **both** the Java controlplane and the Go services serve, so a
caller moves between them by changing an address in config, never its code.

| Contract | Service | RPCs | Servers |
|---|---|---|---|
| `dx/apis/catalogue/v1/item.proto` | `ItemService` | `GetItem` | controlplane, dx-catalogue-go |
| `dx/apis/user/v1/user.proto` | `UserService` | `GetUser`, `GrantRoles`, `RevokeRoles` | controlplane, dx-user-go |
| `dx/apis/acl/v1/policy.proto` | `PolicyService` | `ListActivePolicies`, `CreatePolicy` | controlplane, dx-acl-go |

These are **additive**: the older contracts in `../proto` (`dx.catalogue.v1`,
`dx.user.v1`, `dx.acl.v1`, …) and the Java ones (`org.cdpg.dx.cat.item.v1`,
`org.cdpg.dx.auth.appid.v1`) are unchanged.

## One file, two repos

Each `.proto` here is kept **byte-for-byte identical** in the Java
`dx-common` repo at `src/main/proto/dx/apis/...`, where its Maven build
generates the Java classes. Change both copies in the same pair of PRs, then:

```sh
make check-java JAVA_PROTO=<path to dx-common>/src/main/proto
```

## Rules both servers follow

- **Auth:** `authorization: Bearer <Keycloak service-account token>` metadata;
  the token's `azp` must be on the server's allowlist.
- **Errors:** gRPC status codes only (`NOT_FOUND`, `PERMISSION_DENIED`,
  `ALREADY_EXISTS`, …), never error fields in a response.
- **Compatibility:** never renumber or reuse a field; add new fields and RPCs
  only. A breaking change is a new version (`v2`) package.

## Generating

Generated Go code is checked in. `make proto` regenerates it (needs `protoc`,
`protoc-gen-go`, `protoc-gen-go-grpc`). Callers import, for example:

```go
import cataloguev1 "github.com/datakaveri/dx-common-go/apis/catalogue/v1"
```

Local development across repos needs
`replace github.com/datakaveri/dx-common-go/apis => ../dx-common-go/apis`.
