#!/usr/bin/env bash
set -e

fast='false'
json=''
runtests='false'
runalltests='false'
verbose='false'
experiment='false'
tagsdynamic='false'

usage() {
    echo 'Usage:  build.sh [<flags>]'
    echo ''
    echo 'Builds the "metadb" executable in the bin directory'
    echo ''
    echo 'Flags:'
    echo '-f  Fast build (do not remove executable before compiling)'
    echo '-h  Help'
    echo '-t  Run tests'
    echo '-T  Run tests and other checks; requires'
    echo '    go install golang.org/x/tools/go/analysis/passes/shadow/cmd/shadow@latest'
    # echo '    go install golang.org/x/tools/cmd/deadcode@latest'
    echo '    go install github.com/kisielk/errcheck@latest'
    echo '-v  Enable verbose output'
    # echo '-D  Enable "-tags dynamic" compiler option'
    # echo '-X  Build with experimental code included'
}

while getopts 'fhJtvTD' flag; do
    case "${flag}" in
        t) runtests='true' ;;
        T) runalltests='true' ;;
        f) fast='true' ;;
        J) echo "build.sh: -J option is deprecated" 1>&2 ;;
        h) usage
            exit 1 ;;
        v) verbose='true' ;;
        D) tagsdynamic='true' ;;
        *) usage
            exit 1 ;;
    esac
done

shift $(($OPTIND - 1))
for arg; do
    if [ $arg = 'help' ]
    then
        usage
        exit 1
    fi
    echo "build.sh: unknown argument: $arg" 1>&2
    exit 1
done

if $verbose; then
    v='-v'
fi

if [[ -v METADB_FOLIO ]]; then
    echo "build.sh: using folio reference \"$METADB_FOLIO\"" 1>&2
fi

tags=''

# Check which operating system is running.
case "$(uname -s)" in
    Linux*)     tags='' ;;
    # Darwin*)    tags='-tags dynamic' ;;
    Darwin*)    tags='' ;;
    *)          tags='' ;;
esac

# if $tagsdynamic; then
#     tags='-tags dynamic'
# fi

if $experiment; then
    # echo "The \"build with experimental code\" option (-X) has been selected."
    # read -p "This may prevent later upgrades.  Are you sure? " yn
    # case $yn in
    #     [Yy] ) break ;;
    #     [Yy][Ee][Ss] ) break ;;
    #     * ) echo "Exiting" 1>&2
    #         exit 1 ;;
    # esac
    # json='-X main.rewriteJSON=1'
    if [ -n "$tags" ]; then
        tags="${tags},"
    fi
    tags="${tags}experimental"
    echo "build.sh: building with experimental code" 1>&2
fi

if [ -n "$tags" ]; then
    tags="-tags $tags"
fi

bindir=bin

if ! $fast; then
    rm -f ./$bindir/metadb ./cmd/metadb/parser/gram.go ./cmd/metadb/parser/scan.go ./cmd/metadb/parser/y.output
fi

mkdir -p $bindir

version=`git describe --tags --always`

go generate $v ./...

go build -o $bindir $v $tags -ldflags "-X github.com/metadb-project/metadb/cmd/metadb/util.MetadbVersion=$version -X github.com/metadb-project/metadb/cmd/metadb/util.FolioVersion=$METADB_FOLIO $json" ./cmd/metadb

if $runtests || $runalltests; then
    go test $v $tags -vet=off -count=1 ./cmd/metadb/command 1>&2
#    go test $v $tags -vet=off -count=1 ./cmd/metadb/dbx 1>&2
#    go test $v $tags -vet=off -count=1 ./cmd/metadb/parser 1>&2
#    go test $v $tags -vet=off -count=1 ./cmd/metadb/sqlx 1>&2
    go test $v $tags -vet=off -count=1 ./cmd/metadb/util 1>&2
fi

if $runalltests; then
    go vet $v $tags $(go list ./cmd/... | grep -v 'github.com/metadb-project/metadb/cmd/metadb/parser') 2>&1 | while read s; do echo "build.sh: $s" 1>&2; done
    go vet $v $tags -vettool=$GOPATH/bin/shadow ./cmd/... 2>&1 | while read s; do echo "build.sh: $s" 1>&2; done
    # deadcode -test ./cmd/... 2>&1 | while read s; do echo "build.sh: deadcode: $s" 1>&2; done
    if $verbose; then
        # Using -verbose outputs the function signature for .errcheck.
	verrcheck='-verbose'
    fi
    errcheck $verrcheck -exclude .errcheck ./cmd/... 2>&1 | while read s; do echo "build.sh: errcheck: $s" 1>&2; done
fi
