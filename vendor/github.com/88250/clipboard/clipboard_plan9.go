// Copyright 2013 @atotto. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build plan9
// +build plan9

package clipboard

import (
	"io/ioutil"
	"os"
)

func readFilePaths() (ret []string, err error) {
	ret = []string{}
	data, err := readAll()
	if nil != err {
		return
	}
	ret = append(ret, data)
	return
}

func readAll() (string, error) {
	f, err := os.Open("/dev/snarf")
	if err != nil {
		return "", err
	}
	defer f.Close()

	str, err := ioutil.ReadAll(f)
	if err != nil {
		return "", err
	}

	return string(str), nil
}

func writeAll(text string) error {
	f, err := os.OpenFile("/dev/snarf", os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write([]byte(text))
	if err != nil {
		return err
	}

	return nil
}
