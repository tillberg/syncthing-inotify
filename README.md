#### Building
1. Build syncthing-inotify
 ```
# To clone
mkdir -p src/github.com/syncthing
git clone https://github.com/syncthing/inotify.git src/github.com/syncthing/inotify
# Following commands are needed every time you want to build (unless you use Golang's specific folder structure: C:\src or ~/src/)
export GOPATH=$(pwd)
cd src/github.com/syncthing/inotify
go get
go build
```

2. Run using API key
```
./syncthing-inotify -api="..."
```


#### Troubleshooting
* When watching many files, the OS might not have enough inotify handles available and the app exists with the message:```no space left on device```

  Fix: ```sudo sh -c 'echo 262144 > /proc/sys/fs/inotify/max_user_watches'```
