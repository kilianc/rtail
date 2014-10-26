# rtail

```
$ rtail                                                                                                                         kilian@kBookPro.local (8126/476 Kbps)
Usage: cmd | rtail -h [string] -p [num] [--mute]

Examples:
  server | rtail -h localhost -p 43567 > server.log         remote + file
  server | rtail -h localhost -p 43567                      remote + stdout
  server | rtail -h localhost -p 43567 -m                   only remote
  server | rtail -h localhost -p 43567 -id "prod server"    only remote


Options:
  --mute, -m        don't pipe stdin with stdout
  --host            the recipient server host     [default: "localhost"]
  --port, -p        the recipient server port     [required]
  --id, --name, -n  the log stream id             [default: self assigned]
  --help, -h        Show help
  --version, -v     Show version number

Missing required arguments: port
```