package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/prometheus/alertmanager/template"
	log "github.com/sirupsen/logrus"

	oav1alpha1 "github.com/openshift/ocm-agent-operator/api/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/ocm-agent/pkg/consts"
	"github.com/openshift/ocm-agent/pkg/metrics"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	OCMAgentNamespaceName = "openshift-ocm-agent-operator"
)

type WebhookRHOBSReceiverHandler struct {
	c   client.Client
	ocm OCMClient
}

func NewWebhookRHOBSReceiverHandler(c client.Client, o OCMClient) *WebhookRHOBSReceiverHandler {
	return &WebhookRHOBSReceiverHandler{
		c:   c,
		ocm: o,
	}
}

func (h *WebhookRHOBSReceiverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// validate request
	if r != nil && r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var err error
	var alertData AMReceiverData
	err = json.NewDecoder(r.Body).Decode(&alertData)
	if err != nil {
		log.Errorf("Failed to process request body: %s\n", err)
		http.Error(w, "Bad request body", http.StatusBadRequest)
		metrics.SetRequestMetricFailure(consts.WebhookReceiverPath)
		return
	}

	// process request
	response := h.processAMReceiver(alertData, r.Context())

	// write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.Code)
	err = json.NewEncoder(w).Encode(response)
	if err != nil {
		log.Errorf("Failed to write to response: %s\n", err)
		http.Error(w, "Failed to write to response", http.StatusInternalServerError)
		metrics.SetRequestMetricFailure(consts.WebhookReceiverPath)
		return
	}

	metrics.ResetMetric(metrics.MetricRequestFailure)
}

func (h *WebhookRHOBSReceiverHandler) processAMReceiver(d AMReceiverData, ctx context.Context) *AMReceiverResponse {
	log.WithField("AMReceiverData", fmt.Sprintf("%+v", d)).Info("Process alert data")

	// Handle each firing alert
	for _, alert := range d.Alerts.Firing() {
		// Can we find a notification template for this alert?
		templateName := alert.Labels[AMLabelTemplateName]
		mfn := oav1alpha1.ManagedFleetNotification{}
		//TODO: fix
		err := h.c.Get(ctx, client.ObjectKey{
			Namespace: OCMAgentNamespaceName,
			Name:      templateName,
		}, &mfn)
		if err != nil {
			log.WithError(err).Error("unable to locate corresponding notification template")
			return &AMReceiverResponse{Error: err,
				Status: fmt.Sprintf("unable to find ManagedFleetNotification %s", templateName),
				Code:   http.StatusInternalServerError}
		}

		// Filter actionable alert based on Label
		if !isValidAlert(alert, true) {
			log.WithField(LogFieldAlert, fmt.Sprintf("%+v", alert)).Info("alert does not meet valid criteria")
			continue
		}

		err = h.processAlert(alert, mfn)
		if err != nil {
			log.WithError(err).Error("a firing alert could not be successfully processed")
		}
	}
	return &AMReceiverResponse{Error: nil, Status: "ok", Code: http.StatusOK}
}

// processAlert handles the pre-check verification and sending of a notification for a particular alert
// and returns an error if that process completed successfully or false otherwise
func (h *WebhookRHOBSReceiverHandler) processAlert(alert template.Alert, mfn oav1alpha1.ManagedFleetNotification) error {
	fn := mfn.Spec.FleetNotification
	mcID := alert.Labels[AMLabelAlertMCID]
	hcID := alert.Labels[AMLabelAlertHCID]

	// Fetch the ManagedFleetNotificationRecord, or create it if it does not already exist
	mfnr := &oav1alpha1.ManagedFleetNotificationRecord{}
	err := h.c.Get(context.Background(), client.ObjectKey{
		Namespace: OCMAgentNamespaceName,
		Name:      mcID,
	}, mfnr)
	if err != nil {
		if !errors.IsNotFound(err) {
			log.WithError(err).Error("unable to fetch managedFleetNotificationRecord")
			return fmt.Errorf("unable to fetch managedFleetNotificationRecord for %s", mcID)
		}
		// create ManagedFleetNotificationRecord if not found
		mfnr, err = h.createManagedFleetNotificationRecord(mcID)
		if err != nil {
			log.WithError(err).Error("unable to create managedFleetNotificationRecord")
			return err
		}
	}

	// Verify that our MFNR has a status. If it doesn't, it's a new one, so let's
	// set an initial status.
	if mfnr.Status.ManagementCluster == "" {
		// Set an initial status
		mfnr.Status.ManagementCluster = mcID
		mfnr.Status.NotificationRecordByName = []oav1alpha1.NotificationRecordByName{}

		// Ensure that we can set the initial status successfully
		// (Just in case the rest of the function logic fails)
		err = h.c.Status().Update(context.TODO(), mfnr)
		if err != nil {
			log.WithError(err).Error("unable to set initial managedFleetNotificationRecord status")
			return err
		}
	}

	// Fetch notificationRecordByName and ADD if it doesn't exist
	nfr, err := mfnr.GetNotificationRecordByName(mcID, fn.Name)
	if err != nil {
		//  add NotificationRecordByName
		nfr, err = addNotificationRecordByName(fn.Name, fn.ResendWait, hcID, mfnr)
		if err != nil {
			return err
		}
	}

	// Check if we already have a notification record for this hosted cluster
	_, err = mfnr.GetNotificationRecordItem(mcID, fn.Name, hcID)
	if err != nil {
		// A notification record doesn't exist, so create one
		_, err = mfnr.AddNotificationRecordItem(hcID, nfr)
		if err != nil {
			return err
		}
	}

	// Check if a service log can be sent
	canBeSent, err := mfnr.CanBeSent(mcID, fn.Name, hcID)
	if err != nil {
		log.WithError(err).WithField(LogFieldNotificationName, fn.Name).Error("unable to fetch NotificationrecordByName or NotificationRecordItem")
		return err
	}
	// There's no need to send a service log, so just return
	if !canBeSent {
		log.WithFields(log.Fields{"notification": fn.Name,
			LogFieldResendInterval: fn.ResendWait,
		}).Info("not sending a notification as one was already sent recently")
		return nil
	}

	// Send the servicelog for the alert
	log.WithFields(log.Fields{LogFieldNotificationName: fn.Name}).Info("will send servicelog for notification")
	err = h.ocm.SendServiceLog(fn.Summary, fn.NotificationMessage, "", hcID, fn.Severity, fn.LogType, fn.References, true)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{LogFieldNotificationName: fn.Name, LogFieldIsFiring: true}).Error("unable to send a notification")
		metrics.SetResponseMetricFailure("service_logs")
		return err
	}

	// Reset the metric if we got correct Response from OCM
	metrics.ResetMetric(metrics.MetricResponseFailure)

	// Count the service log sent by the template name
	metrics.CountServiceLogSent(fn.Name, "firing")

	ri, err := mfnr.UpdateNotificationRecordItem(fn.Name, hcID)
	if err != nil || ri == nil {
		log.WithFields(log.Fields{LogFieldNotificationName: fn.Name, LogFieldManagedNotification: mfn.Name}).WithError(err).Error("unable to update notification status in CR")
		return err
	}

	err = h.c.Status().Update(context.TODO(), mfnr)
	if err != nil {
		log.WithFields(log.Fields{LogFieldNotificationName: fn.Name, LogFieldManagedNotification: mfn.Name}).WithError(err).Error("unable to update notification status on cluster")
		return err
	}
	return nil
}

// create ManagedFleetNotificationRecord
func (h *WebhookRHOBSReceiverHandler) createManagedFleetNotificationRecord(mcID string) (*oav1alpha1.ManagedFleetNotificationRecord, error) {
	mfnr := &oav1alpha1.ManagedFleetNotificationRecord{
		ObjectMeta: v1.ObjectMeta{
			Name:      mcID,
			Namespace: OCMAgentNamespaceName,
		},
		Status: oav1alpha1.ManagedFleetNotificationRecordStatus{
			ManagementCluster:        mcID,
			NotificationRecordByName: nil,
		},
	}
	err := h.c.Create(context.Background(), mfnr)
	if err != nil {
		return nil, err
	}
	return mfnr, nil
}

// add NotificationRecordByName for fleetnotification
func addNotificationRecordByName(name string, rswait int32, hcID string, mfrn *oav1alpha1.ManagedFleetNotificationRecord) (*oav1alpha1.NotificationRecordByName, error) {
	nfrbn := oav1alpha1.NotificationRecordByName{
		NotificationName:        name,
		ResendWait:              rswait,
		NotificationRecordItems: nil,
	}
	mfrn.Status.NotificationRecordByName = append(mfrn.Status.NotificationRecordByName, nfrbn)
	_, err := mfrn.AddNotificationRecordItem(hcID, &nfrbn)
	return &nfrbn, err
}
