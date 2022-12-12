package main

import "github.com/thak1411/gorn"

func main() {
	router := gorn.NewRouter()
	router2 := gorn.NewRouter()
	router3 := gorn.NewRouter()
	router.Get("/", func(c *gorn.Context) { // {URL}/
		c.SendPlainText(200, "Hello World")
	})
	router.Get("/middleware/", // {URL}/middleware/
		func(c *gorn.Context) {
			c.SendNotAuthorized()
		},
		func(c *gorn.Context) {
			c.SendPlainText(200, "Hello World")
		},
	)
	router.Get("/middleware2", // {URL}/middleware2
		func(c *gorn.Context) {
			c.SetValue("token", "token value")
		},
		func(c *gorn.Context) {
			c.SendPlainText(200, c.GetValue("token").(string))
		},
	)
	router2.Get("/test", func(c *gorn.Context) { // {URL}/api/test
		c.SendPlainText(200, "test2 get")
	})
	router2.Post("/test", func(c *gorn.Context) { // {URL}/api/test
		type body struct {
			Name string `json:"name"`
		}
		b := &body{}
		if err := c.BindJsonBody(b); err != nil {
			return
		}
		c.SendJson(200, b)
	})
	router3.Get("/test", func(c *gorn.Context) { // {URL}/api/double/test
		c.SendPlainText(200, "test3 get")
	})
	router3.Post("/test", func(c *gorn.Context) { // {URL}/api/double/test
		c.SendPlainText(200, "test3 post")
	})
	router2.Extends("double", router3)
	router.Extends("api", router2)

	if err := router.Run(8081); err != nil {
		panic(err)
	}
}
