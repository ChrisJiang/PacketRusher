/**
 * SPDX-License-Identifier: Apache-2.0
 * © Copyright 2023 Hewlett Packard Enterprise Development LP
 */
package ngap

import (
	log "github.com/sirupsen/logrus"
	"my5G-RANTester/internal/control_test_engine/gnb/context"
	"my5G-RANTester/internal/control_test_engine/gnb/ngap/handler"
	"my5G-RANTester/lib/ngap"
	"my5G-RANTester/lib/ngap/ngapType"
)

func Dispatch(amf *context.GNBAmf, gnb *context.GNBContext, message []byte) {

	if message == nil {
		// TODO return error
		log.Info("[GNB][NGAP] NGAP message is nil")
	}

	// decode NGAP message.
	ngapMsg, err := ngap.Decoder(message)
	if err != nil {
		log.Info("[GNB][NGAP] Error decoding NGAP message in ", gnb.GetGnbId(), " GNB")
	}

	// check RanUeId and get UE.

	// handle NGAP message.
	switch ngapMsg.Present {

	case ngapType.NGAPPDUPresentInitiatingMessage:

		switch ngapMsg.InitiatingMessage.ProcedureCode.Value {

		case ngapType.ProcedureCodeDownlinkNASTransport:
			// handler NGAP Downlink NAS Transport.
			log.Info("[GNB][NGAP] Receive Downlink NAS Transport")
			handler.HandlerDownlinkNasTransport(gnb, ngapMsg)

		case ngapType.ProcedureCodeInitialContextSetup:
			// handler NGAP Initial Context Setup Request.
			log.Info("[GNB][NGAP] Receive Initial Context Setup Request")
			handler.HandlerInitialContextSetupRequest(gnb, ngapMsg)

		case ngapType.ProcedureCodePDUSessionResourceSetup:
			// handler NGAP PDU Session Resource Setup Request.
			log.Info("[GNB][NGAP] Receive PDU Session Resource Setup Request")
			handler.HandlerPduSessionResourceSetupRequest(gnb, ngapMsg)

		case ngapType.ProcedureCodePDUSessionResourceRelease:
			// handler NGAP PDU Session Resource Release
			log.Info("[GNB][NGAP] Receive PDU Session Release Command")
			handler.HandlerPduSessionReleaseCommand(gnb, ngapMsg)

		case ngapType.ProcedureCodeUEContextRelease:
			// handler NGAP UE Context Release
			log.Info("[GNB][NGAP] Receive UE Context Release Command")
			handler.HandlerUeContextReleaseCommand(gnb, ngapMsg)

		case ngapType.ProcedureCodeAMFConfigurationUpdate:
			// handler NGAP AMF Configuration Update
			log.Info("[GNB][NGAP] Receive AMF Configuration Update")
			handler.HandlerAmfConfigurationUpdate(amf, gnb, ngapMsg)

		case ngapType.ProcedureCodeErrorIndication:
			// handler Error Indicator
			log.Error("[GNB][NGAP] Receive Error Indication")
			handler.HandlerErrorIndication(gnb, ngapMsg)

		default:
			log.Info("[GNB][NGAP] Received unknown NGAP message")
		}

	case ngapType.NGAPPDUPresentSuccessfulOutcome:

		switch ngapMsg.SuccessfulOutcome.ProcedureCode.Value {

		case ngapType.ProcedureCodeNGSetup:
			// handler NGAP Setup Response.
			log.Info("[GNB][NGAP] Receive NG Setup Response")
			handler.HandlerNgSetupResponse(amf, gnb, ngapMsg)

		case ngapType.ProcedureCodePathSwitchRequest:
			// handler PathSwitchRequestAcknowledge
			log.Info("[GNB][NGAP] Receive PathSwitchRequestAcknowledge")
			handler.HandlerPathSwitchRequestAcknowledge(gnb, ngapMsg)

		default:
			log.Info("[GNB][NGAP] Received unknown NGAP message")
		}

	case ngapType.NGAPPDUPresentUnsuccessfulOutcome:

		switch ngapMsg.UnsuccessfulOutcome.ProcedureCode.Value {

		case ngapType.ProcedureCodeNGSetup:
			// handler NGAP Setup Failure.
			log.Info("[GNB][NGAP] Receive Ng Setup Failure")
			handler.HandlerNgSetupFailure(amf, gnb, ngapMsg)

		default:
			log.Info("[GNB][NGAP] Received unknown NGAP message")
		}
	}
}
