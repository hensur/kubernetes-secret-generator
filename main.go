/*
 * Copyright 2017 Martin Helmich <m.helmich@mittwald.de>
 *                Mittwald CM Service GmbH & Co. KG
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"flag"
	"github.com/golang/glog"
	"github.com/mittwald/kubernetes-secret-generator/util"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/runtime"
	"k8s.io/client-go/pkg/util/wait"
	"k8s.io/client-go/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"crypto/rand"
	"math/big"
	"time"
)

const (
	SecretGenerateAnnotation    = "secret-generator.v1.mittwald.de/autogenerate"
	SecretGeneratedAtAnnotation = "secret-generator.v1.mittwald.de/autogenerate-generated-at"
	SecretRegenerateAnnotation  = "secret-generator.v1.mittwald.de/regenerate"
)

var runes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

var namespace string
var allNamespaces bool
var kubecfg string
var secretLength int

func main() {
	var config *rest.Config

	flag.StringVar(&kubecfg, "kubeconfig", "", "Path to kubeconfig")
	flag.StringVar(&namespace, "namespace", "default", "Namespace")
	flag.BoolVar(&allNamespaces, "all-namespaces", false, "Watch all namespaces")
	flag.IntVar(&secretLength, "secret-length", 40, "Secret length")

	flag.Parse()

	if kubecfg == "" {
		config, _ = rest.InClusterConfig()
	} else {
		config, _ = clientcmd.BuildConfigFromFlags("", kubecfg)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	gc := GeneratorController{
		client: client,
	}

	if allNamespaces {
		namespace = ""
	}

	_, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(alo api.ListOptions) (runtime.Object, error) {
				var lo v1.ListOptions
				v1.Convert_api_ListOptions_To_v1_ListOptions(&alo, &lo, nil)

				return client.Core().Secrets(namespace).List(lo)
			},
			WatchFunc: func(alo api.ListOptions) (watch.Interface, error) {
				var lo v1.ListOptions
				v1.Convert_api_ListOptions_To_v1_ListOptions(&alo, &lo, nil)

				return client.Core().Secrets(namespace).Watch(lo)
			},
		},
		&v1.Secret{},
		30*time.Minute,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    gc.SecretAdded,
			UpdateFunc: func(old interface{}, new interface{}) { gc.SecretAdded(new) },
			DeleteFunc: func(new interface{}) {},
		},
	)

	gc.controller = controller

	controller.Run(wait.NeverStop)
}

type GeneratorController struct {
	client     kubernetes.Interface
	controller cache.ControllerInterface
}

func (c *GeneratorController) SecretAdded(obj interface{}) {
	secret := obj.(*v1.Secret)

	val, ok := secret.Annotations[SecretGenerateAnnotation]
	if !ok {
		return
	}

	glog.Infof("secret %s is autogenerated", secret.Name)
	regenerateNeeded := false

	if _, ok := secret.Annotations[SecretGeneratedAtAnnotation]; !ok {
		glog.Infof("secret %s does not yet contain autogenerated property", secret.Name)
		regenerateNeeded = true
	}

	if _, ok := secret.Annotations[SecretRegenerateAnnotation]; ok {
		glog.Infof("regenerating of secret %s requested", secret.Name)
		regenerateNeeded = true
	}

	if !regenerateNeeded {
		glog.Infof("secret %s does not need updating", secret.Name)
		return
	}

	secretCopy, err := util.CopyObjToSecret(secret)
	if err != nil {
		glog.Errorf("could not copy secret %s: %s", secret.Name, err)
		return
	}

	newPassword, err := generateSecret(secretLength)
	if err != nil {
		glog.Errorf("could not generate new secret: %s", err)
		return
	}

	if _, ok := secretCopy.Annotations[SecretRegenerateAnnotation]; ok {
		glog.Infof("removing annotation %s from secret %s", SecretRegenerateAnnotation, secret.Name)
		delete(secretCopy.Annotations, SecretRegenerateAnnotation)
	}

	secretCopy.Annotations[SecretGeneratedAtAnnotation] = time.Now().String()
	secretCopy.Data[val] = []byte(newPassword)

	glog.Infof("set value %s of secret %s to new randomly generated secret of %d bytes length", val, secret.Name, secretLength)

	if _, err := c.client.Core().Secrets(secret.Namespace).Update(secretCopy); err != nil {
		glog.Errorf("could not add %s annotation to secret %s: %s", SecretGeneratedAtAnnotation, secret.Name, err)
		return
	}
}

func generateSecret(length int) (string, error) {
	b := make([]rune, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(runes))))
		if err != nil {
			return "", err
		}
		b[i] = runes[n.Int64()]
	}
	return string(b), nil
}
