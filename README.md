1. Build syncthing-inotify
```
export GOPATH=$(pwd)
cd src/syncthing-inotify/
go get
go build
```

2. Run using API key
```
./syncthing-inotify -api="..."
```

