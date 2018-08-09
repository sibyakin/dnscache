### Caching resolver for docker DNS


DISCLAIMER: this library was initially built for internal use in like 2 hours, so code is pretty bad and stability is REALLY not guaranteed. Some people asked me to put it on github, and so I did. 


Main purpose of the library was to bypass docker's libnetwork limit of 100 concurrent queries to the internal resolver by using very simple cache mechanism. We use it with our init-like supervisor in production, and it seems that for our use cases it works pretty well.

Usage:

```go
package main
func main() {
	    resolver := dnscache.NewResolver("127.0.0.1", 53)
	    resolver.Start()
}
```

Especially for user defined docker networks there's a really shitty workaround for updating resolv.conf. It was made into a separate function because it's only needed with user-defined networks or docker-compose usage (this is what actually made me do that https://github.com/docker/compose/issues/2847):

```go
dnscache.ReplaceDockerDns("127.0.0.1") // 127.0.0.1 being your resolver address
```

Docs for options can be found at [godoc]()