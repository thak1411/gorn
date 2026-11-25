# gorn

Rn's Golang Web Server Framework

## Database migrations

MySQL migrations support read-only SQL previews, minimal schema changes, explicit
destructive-change permissions and guarded execution. See [MIGRATIONS.md](MIGRATIONS.md)
for `PlanMigration`, `ApplyMigration`, compatibility changes and local MySQL
tests.

## Installation

```shell
 $ go get -u github.com/thak1411/gorn
```

## Quick Start

```go
package main

import "github.com/thak1411/gorn"

func main() {
    router := gorn.NewRouter()

    router.Get("/", func(c *gorn.Context) {
        c.SendPlainText(200, "Hello, World!")
    })
    router.Run(8080)
}
```
