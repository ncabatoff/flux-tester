#!/bin/sh

set -e

arch=linux_amd64
dldir="`dirname $0`/deps"
bindir="`dirname $0`/bin"

test -d "$dldir" || mkdir "$dldir"
test -d "$bindir" || mkdir "$bindir"

# yq
yq_base=https://github.com/mikefarah/yq/releases/download/
yq_version=1.15.0
yq_relname=yq_$arch
yq_dl=$dldir/$yq_relname-$yq_version 
yq_bin=$bindir/yq

curl -s -L -o $yq_dl -z $yq_dl $yq_base/$yq_version/$yq_relname
chmod 755 $yq_dl
ln -f $yq_dl $yq_bin