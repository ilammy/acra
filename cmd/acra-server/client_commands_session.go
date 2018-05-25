// Copyright 2016, Cossack Labs Limited
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"bufio"
	"github.com/cossacklabs/acra/logging"
	log "github.com/sirupsen/logrus"
	"net"
	"net/http"

	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/cossacklabs/acra/cmd"
	"github.com/cossacklabs/acra/keystore"
	"github.com/cossacklabs/acra/utils"
	"github.com/cossacklabs/acra/zone"
	"github.com/cossacklabs/themis/gothemis/cell"
	"github.com/cossacklabs/themis/gothemis/keys"
	"syscall"
)

const (
	RESPONSE_500_ERROR = "HTTP/1.1 500 Server error\r\n\r\n\r\n\r\n"
)

type ClientCommandsSession struct {
	ClientSession
	Server   *SServer
	keystore keystore.KeyStore
}

func NewClientCommandsSession(keystorage keystore.KeyStore, config *Config, connection net.Conn) (*ClientCommandsSession, error) {
	clientSession, err := NewClientSession(keystorage, config, connection)
	if err != nil {
		return nil, err
	}
	return &ClientCommandsSession{ClientSession: *clientSession, keystore: keystorage}, nil

}

func (clientSession *ClientCommandsSession) ConnectToDb() error {
	return errors.New("command session must not connect to any DB")
}

func (clientSession *ClientCommandsSession) close() {
	log.Debugln("Close acra-connector connection")
	err := clientSession.connection.Close()
	if err != nil {
		log.WithError(err).WithField(logging.FieldKeyEventCode, logging.EventCodeErrorCantCloseConnection).
			Errorln("Error during closing connection to acra-connector")
	}
	log.Debugln("All connections closed")
}

func (clientSession *ClientCommandsSession) HandleSession() {
	reader := bufio.NewReader(clientSession.connection)
	req, err := http.ReadRequest(reader)
	// req = clientSession.connection.Write(*http.ResponseWriter)
	if err != nil {

		log.WithError(err).WithField(logging.FieldKeyEventCode, logging.EventCodeErrorGeneral).
			Warningln("Got new command request, but can't read it")
		clientSession.close()
		return
	}
	response := "HTTP/1.1 404 Not Found\r\n\r\nincorrect request\r\n\r\n"

	log.Debugf("Incoming API request to %v", req.URL.Path)

	switch req.URL.Path {
	case "/getNewZone":
		log.Debugln("Got /getNewZone request")
		id, publicKey, err := clientSession.keystorage.GenerateZoneKey()
		if err == nil {
			zoneData, err := zone.ZoneDataToJson(id, &keys.PublicKey{Value: publicKey})
			if err == nil {
				log.Debugln("Handled request correctly")
				response = fmt.Sprintf("HTTP/1.1 200 OK Found\r\n\r\n%s\r\n\r\n", string(zoneData))
			}
		}
	case "/resetKeyStorage":
		log.Debugln("Got /resetKeyStorage request")
		clientSession.keystorage.Reset()
		response = "HTTP/1.1 200 OK Found\r\n\r\n"
		log.Debugln("Cleared key storage cache")
	case "/loadAuthData":
		response = RESPONSE_500_ERROR
		key, err := clientSession.keystore.GetAuthKey(false)
		if err != nil {
			log.WithError(err).Error("loadAuthData: keystore.GetAuthKey()")
			response = RESPONSE_500_ERROR
			break
		}
		authDataCrypted, err := getAuthDataFromFile(*authPath)
		if err != nil {
			log.Warningf("%v\n", utils.ErrorMessage("loadAuthData: no auth data", err))
			response = RESPONSE_500_ERROR
			break
		}
		SecureCell := cell.New(key, cell.CELL_MODE_SEAL)
		authData, err := SecureCell.Unprotect(authDataCrypted, nil, nil)
		if err != nil {
			log.WithError(err).Error("loadAuthData: SecureCell.Unprotect")

			break
		}
		response = fmt.Sprintf("HTTP/1.1 200 OK Found\r\n\r\n%s\r\n\r\n", authData)
	case "/getConfig":
		log.Debugln("Got /getConfig request")
		jsonOutput, err := clientSession.config.ToJson()
		if err != nil {
			log.WithError(err).WithField(logging.FieldKeyEventCode, logging.EventCodeErrorGeneral).
				Warningln("Can't convert config to JSON")
			response = RESPONSE_500_ERROR
		} else {
			log.Debugln("Handled request correctly")
			log.Debugln(string(jsonOutput))
			response = fmt.Sprintf("HTTP/1.1 200 OK Found\r\n\r\n%s\r\n\r\n", string(jsonOutput))
		}
	case "/setConfig":
		log.Debugln("Got /setConfig request")
		decoder := json.NewDecoder(req.Body)
		var configFromUI UIEditableConfig
		err := decoder.Decode(&configFromUI)
		if err != nil {
			log.WithError(err).WithField(logging.FieldKeyEventCode, logging.EventCodeErrorGeneral).
				Warningln("Can't convert config from incoming")
			response = RESPONSE_500_ERROR
			return
		}
		// set config values
		flag.Set("db_host", configFromUI.DbHost)
		flag.Set("db_port", fmt.Sprintf("%v", configFromUI.DbPort))
		flag.Set("incoming_connection_api_port", fmt.Sprintf("%v", configFromUI.ConnectorApiPort))
		flag.Set("d", fmt.Sprintf("%v", configFromUI.Debug))
		flag.Set("poison_run_script_file", fmt.Sprintf("%v", configFromUI.ScriptOnPoison))
		flag.Set("poison_shutdown_enable", fmt.Sprintf("%v", configFromUI.StopOnPoison))
		flag.Set("zonemode_enable", fmt.Sprintf("%v", configFromUI.WithZone))

		err = cmd.DumpConfig(clientSession.Server.config.GetConfigPath(), SERVICE_NAME, false)
		if err != nil {
			log.WithError(err).WithField(logging.FieldKeyEventCode, logging.EventCodeErrorCantDumpConfig).
				Errorln("DumpConfig failed")
			response = RESPONSE_500_ERROR
			return

		}
		log.Debugln("Handled request correctly, restarting server")
		clientSession.Server.restartSignalsChannel <- syscall.SIGHUP
	}

	_, err = clientSession.connection.Write([]byte(response))
	if err != nil {
		log.WithError(err).WithField(logging.FieldKeyEventCode, logging.EventCodeErrorGeneral).Errorln("Can't send data with secure session to acra-connector")
		return
	}
	clientSession.close()
}