# gorn

Rn's Golang Web Server Framework

## Installation

```shell
 $ go get -u github.com/thak1411/gorn
```

## Quick Start

```go
package main

import "github.com/thak1411/gorn"

func main() {
    router := gorn.New()

    router.Get("/", func(c *gorn.Context) {
        c.SendPlainText(200, "Hello, World!")
    })
    router.Run(8080)
}
```
