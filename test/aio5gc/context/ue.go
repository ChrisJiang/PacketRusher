/**
 * SPDX-License-Identifier: Apache-2.0
 * © Copyright 2023 Hewlett Packard Enterprise Development LP
 */
package context

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/free5gc/nas/nasType"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/ueauth"
	log "github.com/sirupsen/logrus"
)

type UEContext struct {
	ranNgapId                int64
	amfNgapId                int64
	location                 *models.NrLocation
	ueSecurityCapability     *nasType.UESecurityCapability
	ngKsi                    models.NgKsi
	Dnn                      string
	snssai                   models.Snssai
	pei                      string
	securityContext          *SecurityContext
	SecurityContextAvailable bool
	guti                     string
	tmsi                     int32
	smContexts               map[int32]*SmContext
	SmContextMtx             sync.Mutex
}

func (ue *UEContext) AllocateGuti(a *AMFContext) {
	servedGuami := a.servedGuami[0]
	ue.tmsi = a.TmsiAllocate()

	plmnID := servedGuami.PlmnId.Mcc + servedGuami.PlmnId.Mnc
	tmsiStr := fmt.Sprintf("%08x", ue.tmsi)
	ue.guti = plmnID + servedGuami.AmfId + tmsiStr
}

func (ue *UEContext) GetGuti() string {
	return ue.guti
}

func (ue *UEContext) SetRanNgapId(id int64) {
	ue.ranNgapId = id
}

func (ue *UEContext) GetRanNgapId() (id int64) {
	return ue.ranNgapId
}

func (ue *UEContext) SetNssai(nssai models.Snssai) {
	ue.snssai = nssai
}

func (ue *UEContext) GetNssai() models.Snssai {
	return ue.snssai
}

func (ue *UEContext) SetAmfNgapId(id int64) {
	ue.amfNgapId = id
}

func (ue *UEContext) GetAmfNgapId() (id int64) {
	return ue.amfNgapId
}

func (ue *UEContext) SetNgKsi(ksi models.NgKsi) {
	ue.ngKsi = ksi
}

func (ue *UEContext) GetNgKsi() models.NgKsi {
	return ue.ngKsi
}

func (ue *UEContext) SetUserLocationInfo(location *models.NrLocation) {
	ue.location = location
}

func (ue *UEContext) GetUserLocationInfo() *models.NrLocation {
	return ue.location
}

func (ue *UEContext) SetSecurityCapability(capability *nasType.UESecurityCapability) {
	ue.ueSecurityCapability = capability
}

func (ue *UEContext) GetSecurityCapability() *nasType.UESecurityCapability {
	return ue.ueSecurityCapability
}

func (ue *UEContext) GetPei() string {
	return ue.pei
}

func (ue *UEContext) SetPei(pei string) {
	ue.pei = pei
}

func (ue *UEContext) SetSecurityContext(context *SecurityContext) {
	ue.securityContext = context
}

func (ue *UEContext) GetSecurityContext() *SecurityContext {
	return ue.securityContext
}

func (ue *UEContext) AddSmContext(newContext *SmContext) error {
	ue.SmContextMtx.Lock()
	defer ue.SmContextMtx.Unlock()

	sessionId := newContext.GetPduSessionId()
	_, hasKey := ue.smContexts[sessionId]
	if hasKey {
		id := strconv.Itoa(int(sessionId))
		return errors.New("[5GC] Could not create PDU Session " + id + " for UE " + ue.guti + ": already exist")
	} else {
		ue.smContexts[sessionId] = newContext
	}
	return nil
}

func (ue *UEContext) DeleteSmContext(sessionId int32) (SmContext, error) {
	ue.SmContextMtx.Lock()
	defer ue.SmContextMtx.Unlock()

	var smContext SmContext
	_, hasKey := ue.smContexts[sessionId]
	if hasKey {
		smContext = *ue.smContexts[sessionId]
		delete(ue.smContexts, sessionId)
	} else {
		id := strconv.Itoa(int(sessionId))
		return SmContext{}, errors.New("[5GC] Could not delete PDU Session " + id + " for UE " + ue.guti + ": not found")
	}

	return smContext, nil
}

// Kamf Derivation function defined in TS 33.501 Annex A.7
func (ue *UEContext) DerivateKamf() {
	supiRegexp, err := regexp.Compile("(?:imsi|supi)-([0-9]{5,15})")
	if err != nil {
		log.Printf("[5GC] Kamf derivation  %v", err)
		return
	}
	groups := supiRegexp.FindStringSubmatch(ue.securityContext.supi)
	if groups == nil {
		log.Printf("[5GC] Kamf derivation: supi is not correct")
		return
	}

	P0 := []byte(groups[1])
	L0 := ueauth.KDFLen(P0)
	P1 := ue.securityContext.abba
	L1 := ueauth.KDFLen(P1)

	KseafDecode, err := hex.DecodeString(ue.securityContext.kseaf)
	if err != nil {
		log.Printf("[5GC] Kamf derivation  %v", err)
		return
	}
	KamfBytes, err := ueauth.GetKDFValue(KseafDecode, ueauth.FC_FOR_KAMF_DERIVATION, P0, L0, P1, L1)
	if err != nil {
		log.Printf("[5GC] Kamf derivation  %v", err)
		return
	}
	ue.securityContext.kamf = hex.EncodeToString(KamfBytes)
}
