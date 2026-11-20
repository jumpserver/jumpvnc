//go:build !darwin

package main

import "errors"

type unavailablePasswordStore struct{}

func newPasswordStore() passwordStore { return unavailablePasswordStore{} }
func (unavailablePasswordStore) Get(string) (savedPasswords, error) {
	return savedPasswords{}, errPasswordNotFound
}
func (unavailablePasswordStore) Set(string, savedPasswords) error {
	return errors.New("此平台暂不支持保存密码，可取消保存并在连接时输入")
}
func (unavailablePasswordStore) Delete(string) error { return nil }
