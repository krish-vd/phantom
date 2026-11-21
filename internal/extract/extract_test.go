package extract

import (
	"reflect"
	"testing"
)

func keys(ms []Mention) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Key()+":"+string(m.Source))
	}
	return out
}

func TestFromText(t *testing.T) {
	text := "You can pip install the requests library, or run:\n\n" +
		"```bash\n" +
		"pip install \"fastapi[all]>=0.110\" uvicorn==0.29 -r requirements.txt pandas_easy_pivot\n" +
		"python -m pip install --upgrade Flask-Login\n" +
		"npm install --save-dev @types/node@20 react@18 ./local-pkg\n" +
		"yarn add lodash/fp\n" +
		"npx create-react-app my-app\n" +
		"```\n\n" +
		"```python\n" +
		"import os, sys\n" +
		"import numpy as np\n" +
		"from sklearn.model_selection import train_test_split\n" +
		"import cv2\n" +
		"from . import local\n" +
		"from torch_lightning_easy import Trainer\n" +
		"```\n\n" +
		"```ts\n" +
		"import express from 'express'\n" +
		"import { readFile } from 'node:fs/promises'\n" +
		"import path from 'path'\n" +
		"const x = require('jwt-token-validator-easy')\n" +
		"import util from './util'\n" +
		"import Btn from '@/components/Btn'\n" +
		"export { a } from '@scope/pkg/sub'\n" +
		"```\n\n" +
		"Then use `uv add httpx-retry-plus` for retries."

	got := keys(FromText(text))
	want := []string{
		"npm/@scope/pkg:import",
		"npm/@types/node:install",
		"npm/create-react-app:install",
		"npm/express:import",
		"npm/jwt-token-validator-easy:import",
		"npm/lodash:install",
		"npm/react:install",
		"pypi/fastapi:install",
		"pypi/flask-login:install",
		"pypi/httpx-retry-plus:install",
		"pypi/numpy:import",
		"pypi/opencv-python:import",
		"pypi/pandas-easy-pivot:install",
		"pypi/scikit-learn:import",
		"pypi/torch-lightning-easy:import",
		"pypi/uvicorn:install",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%v\nwant\n%v", got, want)
	}
}

func TestProseIsIgnored(t *testing.T) {
	if got := FromText("Just pip install the requests library and npm install express."); len(got) != 0 {
		t.Errorf("prose produced mentions: %v", keys(got))
	}
}

func TestInstallBeatsImport(t *testing.T) {
	text := "```python\nimport requests\n```\n`pip install requests`"
	got := keys(FromText(text))
	if len(got) != 1 || got[0] != "pypi/requests:install" {
		t.Errorf("got %v", got)
	}
}

func TestNormalizePyPI(t *testing.T) {
	for in, want := range map[string]string{"Flask_Login": "flask-login", "ruamel.yaml": "ruamel-yaml", "A--B__c": "a-b-c"} {
		if got := NormalizePyPI(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
