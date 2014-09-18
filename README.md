1. Build syncthing-inotify
 ```
# To clone
mkdir -p src/github.com/Zillode
git clone https://github.com/Zillode/syncthing-inotify.git src/github.com/Zillode/syncthing-inotify
# Following commands are needed every time you want to build (unless you use Golang's specific folder structure: C:\src or ~/src/)
export GOPATH=$(pwd)
cd src/github.com/Zillode/syncthing-inotify
go get
go build
```

2. Run using API key
```
./syncthing-inotify -api="..."
```

