/**
 * SPDX-License-Identifier: Apache-2.0
 * © Copyright 2023 Hewlett Packard Enterprise Development LP
 */

package context

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"my5G-RANTester/internal/control_test_engine/gnb/context"
	"my5G-RANTester/internal/control_test_engine/ue/scenario"
	"my5G-RANTester/lib/UeauCommon"
	"my5G-RANTester/lib/milenage"
	"net"
	"reflect"
	"regexp"
	"sync"

	"github.com/free5gc/nas/nasType"
	"github.com/free5gc/nas/security"

	"my5G-RANTester/internal/common/auth"

	"github.com/free5gc/openapi/models"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

// 5GMM main states in the UE.
const MM5G_NULL = 0x00
const MM5G_DEREGISTERED = 0x01
const MM5G_REGISTERED_INITIATED = 0x02
const MM5G_REGISTERED = 0x03
const MM5G_SERVICE_REQ_INIT = 0x04
const MM5G_DEREGISTERED_INIT = 0x05

// 5GSM main states in the UE.
const SM5G_PDU_SESSION_INACTIVE = 0x06
const SM5G_PDU_SESSION_ACTIVE_PENDING = 0x07
const SM5G_PDU_SESSION_ACTIVE = 0x08

type UEContext struct {
	id         uint8
	UeSecurity SECURITY
	StateMM    int
	gnbRx      chan context.UEMessage
	gnbTx      chan context.UEMessage
	PduSession [16]*UEPDUSession
	amfInfo    Amf

	// TODO: Modify config so you can configure these parameters per PDUSession
	Dnn           string
	Snssai        models.Snssai
	TunnelEnabled bool

	// Sync primitive
	scenarioChan chan scenario.ScenarioMessage

	lock sync.Mutex
}

type Amf struct {
	amfRegionId uint8
	amfSetId    uint16
	amfPointer  uint8
	amfUeId     int64
	mcc         string
	mnc         string
}

type UEPDUSession struct {
	Id            uint8
	GnbPduSession *context.GnbPDUSession
	ueIP          string
	ueGnbIP       net.IP
	tun           netlink.Link
	routeTun      *netlink.Route
	vrf           *netlink.Vrf
	stopSignal    chan bool
	Wait         chan bool
	T3580Retries int

	// TS 24.501 - 6.1.3.2.1.1 State Machine for Session Management
	StateSM int
}

type SECURITY struct {
	Supi                 string
	Msin                 string
	mcc                  string
	mnc                  string
	ULCount              security.Count
	DLCount              security.Count
	UeSecurityCapability *nasType.UESecurityCapability
	IntegrityAlg         uint8
	CipheringAlg         uint8
	Snn                  string
	KnasEnc              [16]uint8
	KnasInt              [16]uint8
	Kamf                 []uint8
	AuthenticationSubs   models.AuthenticationSubscription
	Suci                 nasType.MobileIdentity5GS
	RoutingIndicator     string
	Guti                 [4]byte
}

func (ue *UEContext) NewRanUeContext(msin string,
	ueSecurityCapability *nasType.UESecurityCapability,
	k, opc, op, amf, sqn, mcc, mnc, routingIndicator, dnn string,
	sst int32, sd string, tunnelEnabled bool, scenarioChan chan scenario.ScenarioMessage,
	id uint8) {

	// added SUPI.
	ue.UeSecurity.Msin = msin

	// added ciphering algorithm.
	ue.UeSecurity.UeSecurityCapability = ueSecurityCapability

	integAlg, cipherAlg := auth.SelectAlgorithms(ue.UeSecurity.UeSecurityCapability)

	// set the algorithms of integritys
	ue.UeSecurity.IntegrityAlg = integAlg
	// set the algorithms of ciphering
	ue.UeSecurity.CipheringAlg = cipherAlg

	// added key, AuthenticationManagementField and opc or op.
	ue.SetAuthSubscription(k, opc, op, amf, sqn)

	// added suci
	suciV1, suciV2, suciV3, suciV4, suciV5 := ue.EncodeUeSuci()

	// added mcc and mnc
	ue.UeSecurity.mcc = mcc
	ue.UeSecurity.mnc = mnc

	// add snn
	//ue.UeSecurity.Snn = ue.deriveSNN(mcc, mnc)

	// added routing indidcator
	ue.UeSecurity.RoutingIndicator = routingIndicator

	// added supi
	ue.UeSecurity.Supi = fmt.Sprintf("imsi-%s%s%s", mcc, mnc, msin)

	// added UE id.
	ue.id = id

	// added network slice
	ue.Snssai.Sd = sd
	ue.Snssai.Sst = sst

	// added Domain Network Name.
	ue.Dnn = dnn
	ue.TunnelEnabled = tunnelEnabled

	ue.gnbRx = make(chan context.UEMessage, 1)
	ue.gnbTx = make(chan context.UEMessage, 1)

	// encode mcc and mnc for mobileIdentity5Gs.
	resu := ue.GetMccAndMncInOctets()
	encodedRoutingIndicator := ue.GetRoutingIndicatorInOctets()

	// added suci to mobileIdentity5GS
	if len(ue.UeSecurity.Msin) == 8 {
		ue.UeSecurity.Suci = nasType.MobileIdentity5GS{
			Len:    12,
			Buffer: []uint8{0x01, resu[0], resu[1], resu[2], encodedRoutingIndicator[0], encodedRoutingIndicator[1], 0x00, 0x00, suciV4, suciV3, suciV2, suciV1},
		}
	// Handle both 9 and 10
	//} else if len(ue.UeSecurity.Msin) == 10 {
	} else {
		ue.UeSecurity.Suci = nasType.MobileIdentity5GS{
			Len:    13,
			Buffer: []uint8{0x01, resu[0], resu[1], resu[2], encodedRoutingIndicator[0], encodedRoutingIndicator[1], 0x00, 0x00, suciV5, suciV4, suciV3, suciV2, suciV1},
		}
	}

	ue.scenarioChan = scenarioChan

	// added initial state for MM(NULL)
	ue.StateMM = MM5G_NULL
}

func (ue *UEContext) CreatePDUSession() (*UEPDUSession, error) {
	pduSessionIndex := -1
	for i, pduSession := range ue.PduSession {
		if pduSession == nil {
			pduSessionIndex = i
			break
		}
	}

	if pduSessionIndex == -1 {
		return nil, errors.New("unable to create an additional PDU Session, we already created the max number of PDU Session")
	}

	pduSession := &UEPDUSession{}
	pduSession.Id = uint8(pduSessionIndex + 1)
	pduSession.Wait = make(chan bool)

	ue.PduSession[pduSessionIndex] = pduSession

	return pduSession, nil
}

func (ue *UEContext) GetUeId() uint8 {
	return ue.id
}

func (ue *UEContext) GetSuci() nasType.MobileIdentity5GS {
	return ue.UeSecurity.Suci
}

func (ue *UEContext) GetMsin() string {
	return ue.UeSecurity.Msin
}

func (ue *UEContext) GetSupi() string {
	return ue.UeSecurity.Supi
}

func (ue *UEContext) SetStateMM_DEREGISTERED_INITIATED() {
	ue.StateMM = MM5G_DEREGISTERED_INIT
	ue.scenarioChan <- scenario.ScenarioMessage{StateChange: ue.StateMM}
}

func (ue *UEContext) SetStateMM_MM5G_SERVICE_REQ_INIT() {
	ue.StateMM = MM5G_SERVICE_REQ_INIT
	ue.scenarioChan <- scenario.ScenarioMessage{StateChange: ue.StateMM}
}

func (ue *UEContext) SetStateMM_REGISTERED_INITIATED() {
	ue.StateMM = MM5G_REGISTERED_INITIATED
	ue.scenarioChan <- scenario.ScenarioMessage{StateChange: ue.StateMM}
}

func (ue *UEContext) SetStateMM_REGISTERED() {
	ue.StateMM = MM5G_REGISTERED
	ue.scenarioChan <- scenario.ScenarioMessage{StateChange: ue.StateMM}
}

func (ue *UEContext) SetStateMM_NULL() {
	ue.StateMM = MM5G_NULL
}

func (ue *UEContext) SetStateMM_DEREGISTERED() {
	ue.StateMM = MM5G_DEREGISTERED
	ue.scenarioChan <- scenario.ScenarioMessage{StateChange: ue.StateMM}
}

func (ue *UEContext) GetStateMM() int {
	return ue.StateMM
}

func (ue *UEContext) SetGnbRx(gnbRx chan context.UEMessage) {
	ue.gnbRx = gnbRx
}

func (ue *UEContext) SetGnbTx(gnbTx chan context.UEMessage) {
	ue.gnbTx = gnbTx
}

func (ue *UEContext) GetGnbRx() chan context.UEMessage {
	return ue.gnbRx
}

func (ue *UEContext) GetGnbTx() chan context.UEMessage {
	return ue.gnbTx
}

func (ue *UEContext) Lock() {
	ue.lock.Lock()
}

func (ue *UEContext) Unlock() {
	ue.lock.Unlock()
}

func (ue *UEContext) IsTunnelEnabled() bool {
	return ue.TunnelEnabled
}

func (ue *UEContext) GetPduSession(pduSessionid uint8) (*UEPDUSession, error) {
	if pduSessionid > 15 || ue.PduSession[pduSessionid-1] == nil {
		return nil, errors.New("Unable to find GnbPDUSession ID " + string(pduSessionid))
	}
	return ue.PduSession[pduSessionid-1], nil
}

func (ue *UEContext) GetPduSessions() [16]*context.GnbPDUSession {
	var pduSessions [16]*context.GnbPDUSession

	for i, pduSession := range ue.PduSession {
		if pduSession != nil {
			pduSessions[i] = pduSession.GnbPduSession
		}
	}

	return pduSessions
}

func (ue *UEContext) DeletePduSession(pduSessionid uint8) error {
	if pduSessionid > 15 || ue.PduSession[pduSessionid-1] == nil {
		return errors.New("Unable to find GnbPDUSession ID " + string(pduSessionid))
	}
	pduSession := ue.PduSession[pduSessionid-1]
	close(pduSession.Wait)
	stopSignal := pduSession.GetStopSignal()
	if stopSignal != nil {
		stopSignal <- true
	}
	ue.PduSession[pduSessionid-1] = nil
	return nil
}

func (pduSession *UEPDUSession) SetIp(ip [12]uint8) {
	pduSession.ueIP = fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
}

func (pduSession *UEPDUSession) GetIp() string {
	return pduSession.ueIP
}

func (pduSession *UEPDUSession) SetGnbIp(ip net.IP) {
	pduSession.ueGnbIP = ip
}

func (pduSession *UEPDUSession) GetGnbIp() net.IP {
	return pduSession.ueGnbIP
}

func (pduSession *UEPDUSession) SetStopSignal(stopSignal chan bool) {
	pduSession.stopSignal = stopSignal
}

func (pduSession *UEPDUSession) GetStopSignal() chan bool {
	return pduSession.stopSignal
}

func (pduSession *UEPDUSession) GetPduSesssionId() uint8 {
	return pduSession.Id
}

func (pduSession *UEPDUSession) SetTunInterface(tun netlink.Link) {
	pduSession.tun = tun
}

func (pduSession *UEPDUSession) GetTunInterface() netlink.Link {
	return pduSession.tun
}

func (pduSession *UEPDUSession) SetTunRoute(route *netlink.Route) {
	pduSession.routeTun = route
}

func (pduSession *UEPDUSession) GetTunRoute() *netlink.Route {
	return pduSession.routeTun
}

func (pduSession *UEPDUSession) SetVrfDevice(vrf *netlink.Vrf) {
	pduSession.vrf = vrf
}

func (pduSession *UEPDUSession) GetVrfDevice() *netlink.Vrf {
	return pduSession.vrf
}

func (pdu *UEPDUSession) SetStateSM_PDU_SESSION_INACTIVE() {
	pdu.StateSM = SM5G_PDU_SESSION_INACTIVE
}

func (pdu *UEPDUSession) SetStateSM_PDU_SESSION_ACTIVE() {
	pdu.StateSM = SM5G_PDU_SESSION_ACTIVE
}

func (pdu *UEPDUSession) SetStateSM_PDU_SESSION_PENDING() {
	pdu.StateSM = SM5G_PDU_SESSION_ACTIVE_PENDING
}

func (pduSession *UEPDUSession) GetStateSM() int {
	return pduSession.StateSM
}

func (ue *UEContext) deriveSNN(mcc string, mnc string) string {
	// 5G:mnc093.mcc208.3gppnetwork.org
	var resu string
	if len(mnc) == 2 {
		resu = "5G:mnc0" + mnc + ".mcc" + mcc + ".3gppnetwork.org"
	} else {
		resu = "5G:mnc" + mnc + ".mcc" + mcc + ".3gppnetwork.org"
	}
	return resu
}

func (ue *UEContext) GetUeSecurityCapability() *nasType.UESecurityCapability {
	return ue.UeSecurity.UeSecurityCapability
}

func (ue *UEContext) GetMccAndMncInOctets() []byte {

	// reverse mcc and mnc
	mcc := reverse(ue.UeSecurity.mcc)
	mnc := reverse(ue.UeSecurity.mnc)

	// include mcc and mnc in octets
	oct5 := mcc[1:3]
	var oct6 string
	var oct7 string
	if len(ue.UeSecurity.mnc) == 2 {
		oct6 = "f" + string(mcc[0])
		oct7 = mnc
	} else {
		oct6 = string(mnc[0]) + string(mcc[0])
		oct7 = mnc[1:3]
	}

	// changed for bytes.
	resu, err := hex.DecodeString(oct5 + oct6 + oct7)
	if err != nil {
		fmt.Println(err)
	}

	log.Info("[UE] resu: " + hex.EncodeToString(resu))
	return resu
}

// TS 24.501 9.11.3.4.1
// Routing Indicator shall consist of 1 to 4 digits. The coding of this field is the
// responsibility of home network operator but BCD coding shall be used. If a network
// operator decides to assign less than 4 digits to Routing Indicator, the remaining digits
// shall be coded as "1111" to fill the 4 digits coding of Routing Indicator (see NOTE 2). If
// no Routing Indicator is configured in the USIM, the UE shall coxde bits 1 to 4 of octet 8
// of the Routing Indicator as "0000" and the remaining digits as “1111".
func (ue *UEContext) GetRoutingIndicatorInOctets() []byte {
	if len(ue.UeSecurity.RoutingIndicator) == 0 {
		ue.UeSecurity.RoutingIndicator = "0"
	}

	if len(ue.UeSecurity.RoutingIndicator) > 4 {
		log.Fatal("[UE][CONFIG] Routing indicator must be 4 digits maximum, ", ue.UeSecurity.RoutingIndicator, " is invalid")
	}

	routingIndicator := []byte(ue.UeSecurity.RoutingIndicator)
	for len(routingIndicator) < 4 {
		routingIndicator = append(routingIndicator, 'F')
	}

	// Reverse the bytes in group of two
	for i := 1; i < len(routingIndicator); i += 2 {
		tmp := routingIndicator[i-1]
		routingIndicator[i-1] = routingIndicator[i]
		routingIndicator[i] = tmp
	}

	// BCD conversion
	encodedRoutingIndicator, err := hex.DecodeString(string(routingIndicator))
	if err != nil {
		log.Fatal("[UE][CONFIG] Unable to encode routing indicator ", err)
	}

	return encodedRoutingIndicator
}

func (ue *UEContext) EncodeUeSuci() (uint8, uint8, uint8, uint8, uint8) {

	// reverse imsi string.
	aux := reverse(ue.UeSecurity.Msin)

	// prefix 0 if the original MSIN is not even
	if len(aux) % 2 != 0 {
		aux = "f" + aux
	}

	// calculate decimal value.
	suci, error := hex.DecodeString(aux)
	if error != nil {
		return 0, 0, 0, 0, 0
	}

	// return decimal value
	// Function worked fine.
	if len(ue.UeSecurity.Msin) == 8 {
		return uint8(suci[0]), uint8(suci[1]), uint8(suci[2]), uint8(suci[3]), 0
	} else {
		return uint8(suci[0]), uint8(suci[1]), uint8(suci[2]), uint8(suci[3]), uint8(suci[4])
	}
}

func (ue *UEContext) SetAmfRegionId(amfRegionId uint8) {
	ue.amfInfo.amfRegionId = amfRegionId
}

func (ue *UEContext) GetAmfRegionId() uint8 {
	return ue.amfInfo.amfRegionId
}

func (ue *UEContext) SetAmfPointer(amfPointer uint8) {
	ue.amfInfo.amfPointer = amfPointer
}

func (ue *UEContext) GetAmfPointer() uint8 {
	return ue.amfInfo.amfPointer
}

func (ue *UEContext) SetAmfSetId(amfSetId uint16) {
	ue.amfInfo.amfSetId = amfSetId
}

func (ue *UEContext) GetAmfSetId() uint16 {
	return ue.amfInfo.amfSetId
}

func (ue *UEContext) SetAmfUeId(id int64) {
	ue.amfInfo.amfUeId = id
}

func (ue *UEContext) GetAmfUeId() int64 {
	return ue.amfInfo.amfUeId
}

func (ue *UEContext) SetAmfMccAndMnc(mcc string, mnc string) {
	ue.amfInfo.mcc = mcc
	ue.amfInfo.mnc = mnc
	//not using AMF info for SNN ?
	ue.UeSecurity.Snn = ue.deriveSNN(mcc, mnc)
}

func (ue *UEContext) Get5gGuti() [4]uint8 {
	return ue.UeSecurity.Guti
}

func (ue *UEContext) Set5gGuti(guti [4]uint8) {
	ue.UeSecurity.Guti = guti
}

func (ue *UEContext) deriveAUTN(autn []byte, ak []uint8) ([]byte, []byte, []byte) {

	sqn := make([]byte, 6)

	// get SQNxorAK
	SQNxorAK := autn[0:6]
	amf := autn[6:8]
	mac_a := autn[8:]

	// get SQN
	for i := 0; i < len(SQNxorAK); i++ {
		sqn[i] = SQNxorAK[i] ^ ak[i]
	}

	// return SQN, amf, mac_a
	return sqn, amf, mac_a
}

func (ue *UEContext) DeriveRESstarAndSetKey(authSubs models.AuthenticationSubscription,
	RAND []byte,
	snName string,
	AUTN []byte) ([]byte, string) {

	// parameters for authentication challenge.
	mac_a, mac_s := make([]byte, 8), make([]byte, 8)
	CK, IK := make([]byte, 16), make([]byte, 16)
	RES := make([]byte, 8)
	AK, AKstar := make([]byte, 6), make([]byte, 6)

	// Get OPC, K, SQN, AMF from USIM.
	OPC, err := hex.DecodeString(authSubs.Opc.OpcValue)
	if err != nil {
		log.Fatal("[UE] OPC error: ", err, authSubs.Opc.OpcValue)
	}
	K, err := hex.DecodeString(authSubs.PermanentKey.PermanentKeyValue)
	if err != nil {
		log.Fatal("[UE] K error: ", err, authSubs.PermanentKey.PermanentKeyValue)
	}
	sqnUe, err := hex.DecodeString(authSubs.SequenceNumber)
	if err != nil {
		log.Fatal("[UE] sqn error: ", err, authSubs.SequenceNumber)
	}
	AMF, err := hex.DecodeString(authSubs.AuthenticationManagementField)
	if err != nil {
		log.Fatal("[UE] AuthenticationManagementField error: ", err, authSubs.AuthenticationManagementField)
	}

	log.Info("OPC: " + hex.EncodeToString(OPC))
	log.Info("K: " + hex.EncodeToString(K))
	log.Info("sqnUe: " + hex.EncodeToString(sqnUe))
	log.Info("AMF: " + hex.EncodeToString(AMF))
	log.Info("RAND: " + hex.EncodeToString(RAND))
	log.Info("snName: " + snName)
	log.Info("AUTN: " + hex.EncodeToString(AUTN))

	// Generate RES, CK, IK, AK, AKstar
	milenage.F2345_Test(OPC, K, RAND, RES, CK, IK, AK, AKstar)

	log.Info("RES: " + hex.EncodeToString(RES))
	log.Info("CK: " + hex.EncodeToString(CK))
	log.Info("IK: " + hex.EncodeToString(IK))
	log.Info("AK: " + hex.EncodeToString(AK))
	log.Info("AKstar: " + hex.EncodeToString(AKstar))

	// Get SQN, MAC_A, AMF from AUTN
	sqnHn, _, mac_aHn := ue.deriveAUTN(AUTN, AK)

	log.Info("sqnHn: " + hex.EncodeToString(sqnHn))
	log.Info("mac_aHn: " + hex.EncodeToString(mac_aHn))

	// Generate MAC_A, MAC_S
	milenage.F1_Test(OPC, K, RAND, sqnHn, AMF, mac_a, mac_s)

	log.Info("mac_a: " + hex.EncodeToString(mac_a))
	log.Info("mac_s: " + hex.EncodeToString(mac_s))

	// MAC verification.
	if !reflect.DeepEqual(mac_a, mac_aHn) {
		log.Warn("Ignoring MAC failure mac_a: " + hex.EncodeToString(mac_a) + " mac_aHn: " + hex.EncodeToString(mac_aHn))
		//return nil, "MAC failure"
	}

	// Verification of sequence number freshness.
	if bytes.Compare(sqnUe, sqnHn) > 0 {

		// get AK*
		milenage.F2345_Test(OPC, K, RAND, RES, CK, IK, AK, AKstar)

		// From the standard, AMF(0x0000) should be used in the synch failure.
		amfSynch, _ := hex.DecodeString("0000")

		// get mac_s using sqn ue.
		milenage.F1_Test(OPC, K, RAND, sqnUe, amfSynch, mac_a, mac_s)

		sqnUeXorAK := make([]byte, 6)
		for i := 0; i < len(sqnUe); i++ {
			sqnUeXorAK[i] = sqnUe[i] ^ AKstar[i]
		}

		failureParam := append(sqnUeXorAK, mac_s...)

		return failureParam, "SQN failure"
	}

	// updated sqn value.
	authSubs.SequenceNumber = fmt.Sprintf("%x", sqnHn)

	// derive RES*
	key := append(CK, IK...)
	FC := UeauCommon.FC_FOR_RES_STAR_XRES_STAR_DERIVATION
	P0 := []byte(snName)
	P1 := RAND
	P2 := RES

	ue.DerivateKamf(key, snName, sqnHn, AK)
	ue.DerivateAlgKey()
	kdfVal_for_resStar := UeauCommon.GetKDFValue(key, FC, P0, UeauCommon.KDFLen(P0), P1, UeauCommon.KDFLen(P1), P2, UeauCommon.KDFLen(P2))
	return kdfVal_for_resStar[len(kdfVal_for_resStar)/2:], "successful"
}

func (ue *UEContext) DerivateKamf(key []byte, snName string, SQN, AK []byte) {

	FC := UeauCommon.FC_FOR_KAUSF_DERIVATION
	P0 := []byte(snName)
	SQNxorAK := make([]byte, 6)
	for i := 0; i < len(SQN); i++ {
		SQNxorAK[i] = SQN[i] ^ AK[i]
	}
	P1 := SQNxorAK
	Kausf := UeauCommon.GetKDFValue(key, FC, P0, UeauCommon.KDFLen(P0), P1, UeauCommon.KDFLen(P1))
	P0 = []byte(snName)
	Kseaf := UeauCommon.GetKDFValue(Kausf, UeauCommon.FC_FOR_KSEAF_DERIVATION, P0, UeauCommon.KDFLen(P0))

	supiRegexp, _ := regexp.Compile("(?:imsi|supi)-([0-9]{5,15})")
	groups := supiRegexp.FindStringSubmatch(ue.UeSecurity.Supi)

	log.Info("[DerivateKamf] P0: " + groups[1])

	P0 = []byte(groups[1])
	L0 := UeauCommon.KDFLen(P0)
	P1 = []byte{0x00, 0x00}
	L1 := UeauCommon.KDFLen(P1)

	ue.UeSecurity.Kamf = UeauCommon.GetKDFValue(Kseaf, UeauCommon.FC_FOR_KAMF_DERIVATION, P0, L0, P1, L1)
}

func (ue *UEContext) DerivateAlgKey() {

	err := auth.AlgorithmKeyDerivation(ue.UeSecurity.CipheringAlg,
		ue.UeSecurity.Kamf,
		&ue.UeSecurity.KnasEnc,
		ue.UeSecurity.IntegrityAlg,
		&ue.UeSecurity.KnasInt)

	if err != nil {
		log.Errorf("[UE] Algorithm key derivation failed  %v", err)
	}
}

func (ue *UEContext) SetAuthSubscription(k, opc, op, amf, sqn string) {
	ue.UeSecurity.AuthenticationSubs.PermanentKey = &models.PermanentKey{
		PermanentKeyValue: k,
	}
	ue.UeSecurity.AuthenticationSubs.Opc = &models.Opc{
		OpcValue: opc,
	}
	ue.UeSecurity.AuthenticationSubs.Milenage = &models.Milenage{
		Op: &models.Op{
			OpValue: op,
		},
	}
	ue.UeSecurity.AuthenticationSubs.AuthenticationManagementField = amf

	ue.UeSecurity.AuthenticationSubs.SequenceNumber = sqn
	ue.UeSecurity.AuthenticationSubs.AuthenticationMethod = models.AuthMethod__5_G_AKA
}

func (ue *UEContext) Terminate() {
	ue.SetStateMM_NULL()

	// clean all context of tun interface
	for _, pduSession := range ue.PduSession {
		if pduSession != nil {
			ueTun := pduSession.GetTunInterface()
			ueRoute := pduSession.GetTunRoute()
			ueVrf := pduSession.GetVrfDevice()

			if ueTun != nil {
				_ = netlink.LinkSetDown(ueTun)
				_ = netlink.LinkDel(ueTun)
			}

			if ueRoute != nil {
				_ = netlink.RouteDel(ueRoute)
			}

			if ueVrf != nil {
				_ = netlink.LinkSetDown(ueVrf)
				_ = netlink.LinkDel(ueVrf)
			}
		}
	}

	ue.Lock()
	if ue.gnbRx != nil {
		close(ue.gnbRx)
		ue.gnbRx = nil
	}
	ue.Unlock()
	close(ue.scenarioChan)

	log.Info("[UE] UE Terminated")
}

func reverse(s string) string {
	// reverse string.
	var aux string
	for _, valor := range s {
		aux = string(valor) + aux
	}
	return aux

}
