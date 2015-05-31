#!/bin/sh


gpath=`echo $GOPATH`

sudo env GOPATH="$gpath" gox -build-toolchain
cd bin/ && sudo env GOPATH="$gpath" gox ../ && cd ..
