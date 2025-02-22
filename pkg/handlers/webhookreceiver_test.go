package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"fmt"
	"io"
	"net/http"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/golang/mock/gomock"
	"github.com/prometheus/alertmanager/template"

	corev1 "k8s.io/api/core/v1"
	k8serrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ocmagentv1alpha1 "github.com/openshift/ocm-agent-operator/api/v1alpha1"

	testconst "github.com/openshift/ocm-agent/pkg/consts/test"
	webhookreceivermock "github.com/openshift/ocm-agent/pkg/handlers/mocks"
	clientmocks "github.com/openshift/ocm-agent/pkg/util/test/generated/mocks/client"
)

type RoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

var _ = Describe("Webhook Handlers", func() {

	var (
		mockCtrl                    *gomock.Controller
		mockClient                  *clientmocks.MockClient
		mockStatusWriter            *clientmocks.MockStatusWriter
		mockOCMClient               *webhookreceivermock.MockOCMClient
		webhookReceiverHandler      *WebhookReceiverHandler
		server                      *ghttp.Server
		testAlert                   template.Alert
		testAlertResolved           template.Alert
		testManagedNotificationList *ocmagentv1alpha1.ManagedNotificationList
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = clientmocks.NewMockClient(mockCtrl)
		mockStatusWriter = clientmocks.NewMockStatusWriter(mockCtrl)
		server = ghttp.NewServer()
		mockOCMClient = webhookreceivermock.NewMockOCMClient(mockCtrl)
		webhookReceiverHandler = &WebhookReceiverHandler{
			c:   mockClient,
			ocm: mockOCMClient,
		}
		testAlert = testconst.NewTestAlert(false, false)
		testAlertResolved = testconst.NewTestAlert(true, false)
	})
	AfterEach(func() {
		server.Close()
	})
	Context("AMReceiver processAMReceiver", func() {
		var r http.Request
		BeforeEach(func() {
			mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
		})
		It("Returns the correct AMReceiverResponse", func() {
			alert := AMReceiverData{
				Status: "foo",
			}
			response := webhookReceiverHandler.processAMReceiver(alert, r.Context())
			expect := AMReceiverResponse{
				Error:  nil,
				Code:   200,
				Status: "ok",
			}
			Expect(response.Status).Should(Equal(expect.Status))
		})
	})
	Context("AMReceiver handler post", func() {
		var resp *http.Response
		var err error
		BeforeEach(func() {
			// add handler to the server
			server.AppendHandlers(webhookReceiverHandler.ServeHTTP)
			// Expect call *client.List(arg1, arg2, arg3) on mocked WebhookReceiverHandler
			mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
			// Set data to post
			postData := AMReceiverData{
				Status: "foo",
			}
			// convert AMReceiverData to json for http request
			postDataJson, _ := json.Marshal(postData)
			// post to AMReceiver handler
			resp, err = http.Post(server.URL(), "application/json", bytes.NewBuffer(postDataJson))
		})
		It("Returns the correct http status code", func() {
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		})
		It("Returns the correct content type", func() {
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.Header.Get("Content-Type")).Should(Equal("application/json"))
		})
		It("Returns the correct response", func() {
			Expect(err).ShouldNot(HaveOccurred())
			// Set expected
			expected := AMReceiverResponse{
				Status: "ok",
				Code:   200,
				Error:  nil,
			}
			// Set response
			var response AMReceiverResponse
			_ = json.NewDecoder(resp.Body).Decode(&response)
			Expect(response).Should(Equal(expected))
		})
	})
	Context("AMReceiver handler post bad data", func() {
		var resp *http.Response
		var err error
		BeforeEach(func() {
			// add handler to the server
			server.AppendHandlers(webhookReceiverHandler.ServeHTTP)
			// Set data to post
			postData := ""
			// convert AMReceiverData to json for http request
			postDataJson, _ := json.Marshal(postData)
			// post to AMReceiver handler
			resp, err = http.Post(server.URL(), "application/json", bytes.NewBuffer(postDataJson))
		})
		It("Returns the correct http status code", func() {
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).Should(Equal(http.StatusBadRequest))
		})
		It("Returns the correct content type", func() {
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.Header.Get("Content-Type")).Should(Equal("text/plain; charset=utf-8"))
		})
		It("Returns the correct response", func() {
			Expect(err).ShouldNot(HaveOccurred())
			body, _ := io.ReadAll(resp.Body)
			Expect(string(body)).Should(Equal("Bad request body\n"))
		})
	})
	Context("AMReceiver handler get", func() {
		var resp *http.Response
		var err error
		BeforeEach(func() {
			// add handler to the server
			server.AppendHandlers(webhookReceiverHandler.ServeHTTP)
			resp, err = http.Get(server.URL())
		})
		It("Returns the correct http status code", func() {
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).Should(Equal(http.StatusMethodNotAllowed))
		})
		It("Returns the correct content type", func() {
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.Header.Get("Content-Type")).Should(Equal("text/plain; charset=utf-8"))
		})
		It("Returns the correct response", func() {
			Expect(err).ShouldNot(HaveOccurred())
			body, _ := io.ReadAll(resp.Body)
			Expect(string(body)).Should(Equal("Method Not Allowed\n"))
		})
	})

	Context("When looking for a matching notification for an alert", func() {
		It("will return one if one exists", func() {
			n, mn, err := getNotification(testconst.TestNotificationName, testconst.TestManagedNotificationList)
			Expect(err).To(BeNil())
			Expect(reflect.DeepEqual(*n, testconst.TestNotification)).To(BeTrue())
			Expect(reflect.DeepEqual(*mn, testconst.TestManagedNotification)).To(BeTrue())
		})
		It("will return nil if one does not exist", func() {
			_, _, err := getNotification("dummy-nonexistent-test", testconst.TestManagedNotificationList)
			Expect(err).ToNot(BeNil())
		})
	})

	Context("When processing an alert", func() {
		Context("Check if an alert is valid or not", func() {
			It("Reports error if alert does not have alertname label", func() {
				delete(testAlert.Labels, "alertname")
				err := webhookReceiverHandler.processAlert(testAlert, testconst.TestManagedNotificationList, true)
				Expect(err).Should(HaveOccurred())
			})
			It("Reports error if alert does not have managed_notification_template label", func() {
				delete(testAlert.Labels, "managed_notification_template")
				err := webhookReceiverHandler.processAlert(testAlert, testconst.TestManagedNotificationList, true)
				Expect(err).Should(HaveOccurred())
			})
			It("Reports error if alert does not have send_managed_notification label", func() {
				delete(testAlert.Labels, "send_managed_notification")
				err := webhookReceiverHandler.processAlert(testAlert, testconst.TestManagedNotificationList, true)
				Expect(err).Should(HaveOccurred())
			})
		})
		Context("Check if a valid alert can be mapped to existing notification template definition or not", func() {
			BeforeEach(func() {
				testAlert = template.Alert{
					Status: "firing",
					Labels: map[string]string{
						"managed_notification_template": "test-notification",
						"send_managed_notification":     "true",
						"alertname":                     "TestAlertName",
					},
					StartsAt: time.Now(),
					EndsAt:   time.Time{},
				}
			})
			It("Reports failure if cannot fetch notification for a valid alert", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{}
				err := webhookReceiverHandler.processAlert(testAlert, testManagedNotificationList, true)
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Check if servicelog can be sent for an alert or not", func() {
			BeforeEach(func() {
				testAlert = template.Alert{
					Status: "firing",
					Labels: map[string]string{
						"managed_notification_template": "test-notification",
						"send_managed_notification":     "true",
						"alertname":                     "TestAlertName",
						"alertstate":                    "firing",
					},
				}
			})
			It("Should not send service log for a firing alert if one is already sent within resend time", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{
					Items: []ocmagentv1alpha1.ManagedNotification{
						{
							Spec: ocmagentv1alpha1.ManagedNotificationSpec{
								Notifications: []ocmagentv1alpha1.Notification{
									testconst.TestNotification,
								},
							},
							Status: ocmagentv1alpha1.ManagedNotificationStatus{
								NotificationRecords: ocmagentv1alpha1.NotificationRecords{
									ocmagentv1alpha1.NotificationRecord{
										Name:                "test-notification",
										ServiceLogSentCount: 0,
										Conditions: []ocmagentv1alpha1.NotificationCondition{
											{
												Type:               ocmagentv1alpha1.ConditionServiceLogSent,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
										},
									},
								},
							},
						},
					},
				}
				err := webhookReceiverHandler.processAlert(testAlert, testManagedNotificationList, true)
				Expect(err).ShouldNot(HaveOccurred())
			})
			It("Should send service log for a firing alert if one hasn't already sent after resend time and update notification", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{
					Items: []ocmagentv1alpha1.ManagedNotification{
						{
							Spec: ocmagentv1alpha1.ManagedNotificationSpec{
								Notifications: []ocmagentv1alpha1.Notification{
									testconst.TestNotification,
								},
							},
							Status: ocmagentv1alpha1.ManagedNotificationStatus{
								NotificationRecords: ocmagentv1alpha1.NotificationRecords{
									ocmagentv1alpha1.NotificationRecord{
										Name:                "test-notification",
										ServiceLogSentCount: 0,
										Conditions: []ocmagentv1alpha1.NotificationCondition{
											{
												Type:               ocmagentv1alpha1.ConditionAlertFiring,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionAlertResolved,
												Status:             corev1.ConditionFalse,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionServiceLogSent,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now().Add(time.Duration(-90) * time.Minute)},
											},
										},
									},
								},
							},
						},
					},
				}
				gomock.InOrder(
					mockOCMClient.EXPECT().SendServiceLog(
						testconst.TestNotification.Summary,
						testconst.TestNotification.ActiveDesc,
						testconst.TestNotification.ResolvedDesc,
						gomock.Any(), // cluster id
						testconst.TestNotification.Severity,
						testconst.TestNotification.LogType,
						testconst.TestNotification.References,
						true, // firing
					),
					mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testManagedNotificationList.Items[0]),
					mockClient.EXPECT().Status().Return(mockStatusWriter),
					mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
				)
				err := webhookReceiverHandler.processAlert(testAlert, testManagedNotificationList, true)
				Expect(err).ShouldNot(HaveOccurred())
			})
			It("Should not send servicelog if the alert was not in firing state and is resolved", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{
					Items: []ocmagentv1alpha1.ManagedNotification{
						{
							Spec: ocmagentv1alpha1.ManagedNotificationSpec{
								Notifications: []ocmagentv1alpha1.Notification{
									testconst.TestNotification,
								},
							},
							Status: ocmagentv1alpha1.ManagedNotificationStatus{
								NotificationRecords: ocmagentv1alpha1.NotificationRecords{
									ocmagentv1alpha1.NotificationRecord{
										Name:                "test-notification",
										ServiceLogSentCount: 0,
										Conditions: []ocmagentv1alpha1.NotificationCondition{
											{
												Type:               ocmagentv1alpha1.ConditionAlertFiring,
												Status:             corev1.ConditionFalse,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionAlertResolved,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionServiceLogSent,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now().Add(time.Duration(-90) * time.Minute)},
											},
										},
									},
								},
							},
						},
					},
				}
				err := webhookReceiverHandler.processAlert(testAlert, testManagedNotificationList, false)
				Expect(err).ShouldNot(HaveOccurred())
			})
			It("Should not send resolved servicelog if the resolved body is empty", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{
					Items: []ocmagentv1alpha1.ManagedNotification{
						{
							Spec: ocmagentv1alpha1.ManagedNotificationSpec{
								Notifications: []ocmagentv1alpha1.Notification{
									testconst.NotificationWithoutResolvedBody,
								},
							},
							Status: ocmagentv1alpha1.ManagedNotificationStatus{
								NotificationRecords: ocmagentv1alpha1.NotificationRecords{
									ocmagentv1alpha1.NotificationRecord{
										Name:                "test-notification",
										ServiceLogSentCount: 1,
										Conditions: []ocmagentv1alpha1.NotificationCondition{
											{
												Type:               ocmagentv1alpha1.ConditionAlertFiring,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionAlertResolved,
												Status:             corev1.ConditionFalse,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionServiceLogSent,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
										},
									},
								},
							},
						},
					},
				}
				gomock.InOrder(
					mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testManagedNotificationList.Items[0]),
					mockClient.EXPECT().Status().Return(mockStatusWriter),
					mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
				)
				err := webhookReceiverHandler.processAlert(testAlertResolved, testManagedNotificationList, false)
				Expect(err).ShouldNot(HaveOccurred())
			})
			It("Should report error if not able to send service log", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{
					Items: []ocmagentv1alpha1.ManagedNotification{
						{
							Spec: ocmagentv1alpha1.ManagedNotificationSpec{
								Notifications: []ocmagentv1alpha1.Notification{
									testconst.TestNotification,
								},
							},
							Status: ocmagentv1alpha1.ManagedNotificationStatus{
								NotificationRecords: ocmagentv1alpha1.NotificationRecords{
									ocmagentv1alpha1.NotificationRecord{
										Name:                "test-notification",
										ServiceLogSentCount: 0,
										Conditions: []ocmagentv1alpha1.NotificationCondition{
											{
												Type:               ocmagentv1alpha1.ConditionAlertFiring,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionAlertResolved,
												Status:             corev1.ConditionFalse,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionServiceLogSent,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now().Add(time.Duration(-90) * time.Minute)},
											},
										},
									},
								},
							},
						},
					},
				}
				gomock.InOrder(
					mockOCMClient.EXPECT().SendServiceLog(
						testconst.TestNotification.Summary,
						testconst.TestNotification.ActiveDesc,
						testconst.TestNotification.ResolvedDesc,
						gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), true).Return(k8serrs.NewInternalError(fmt.Errorf("a fake error"))),
				)
				err := webhookReceiverHandler.processAlert(testAlert, testManagedNotificationList, true)
				Expect(err).Should(HaveOccurred())
			})
			It("Should report error if not able to update NotificationStatus", func() {
				testManagedNotificationList = &ocmagentv1alpha1.ManagedNotificationList{
					Items: []ocmagentv1alpha1.ManagedNotification{
						{
							Spec: ocmagentv1alpha1.ManagedNotificationSpec{
								Notifications: []ocmagentv1alpha1.Notification{
									testconst.TestNotification,
								},
							},
							Status: ocmagentv1alpha1.ManagedNotificationStatus{
								NotificationRecords: ocmagentv1alpha1.NotificationRecords{
									ocmagentv1alpha1.NotificationRecord{
										Name:                "test-notification",
										ServiceLogSentCount: 0,
										Conditions: []ocmagentv1alpha1.NotificationCondition{
											{
												Type:               ocmagentv1alpha1.ConditionAlertFiring,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionAlertResolved,
												Status:             corev1.ConditionFalse,
												LastTransitionTime: &metav1.Time{Time: time.Now()},
											},
											{
												Type:               ocmagentv1alpha1.ConditionServiceLogSent,
												Status:             corev1.ConditionTrue,
												LastTransitionTime: &metav1.Time{Time: time.Now().Add(time.Duration(-90) * time.Minute)},
											},
										},
									},
								},
							},
						},
					},
				}
				gomock.InOrder(
					mockOCMClient.EXPECT().SendServiceLog(
						testconst.TestNotification.Summary,
						testconst.TestNotification.ActiveDesc,
						testconst.TestNotification.ResolvedDesc,
						gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), true).Return(nil),
					mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testManagedNotificationList.Items[0]),
					mockClient.EXPECT().Status().Return(mockStatusWriter),
					mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(k8serrs.NewInternalError(fmt.Errorf("a fake error"))),
				)
				err := webhookReceiverHandler.processAlert(testAlert, testManagedNotificationList, true)
				Expect(err).Should(HaveOccurred())
			})
		})
	})

	Context("When updating Notification status", func() {
		It("Report error if not able to get ManagedNotification", func() {
			fakeError := k8serrs.NewInternalError(fmt.Errorf("a fake error"))
			gomock.InOrder(
				mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeError),
			)
			_, err := webhookReceiverHandler.updateNotificationStatus(&testconst.TestNotification, &testconst.TestManagedNotification, true)
			Expect(err).ShouldNot(BeNil())
		})
		When("Getting NotificationRecord for which status does not exist", func() {
			It("should create status if NotificationRecord not found", func() {
				gomock.InOrder(
					mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testconst.TestManagedNotificationWithoutStatus),
					mockClient.EXPECT().Status().Return(mockStatusWriter),
					mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
						func(ctx context.Context, mn *ocmagentv1alpha1.ManagedNotification, client ...client.UpdateOptions) error {
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionAlertFiring).Status).To(Equal(corev1.ConditionTrue))
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionAlertResolved).Status).To(Equal(corev1.ConditionFalse))
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionServiceLogSent).Status).To(Equal(corev1.ConditionTrue))
							return nil
						}),
				)
				_, err := webhookReceiverHandler.updateNotificationStatus(&ocmagentv1alpha1.Notification{Name: "randomnotification"}, &testconst.TestManagedNotificationWithoutStatus, true)
				Expect(err).Should(BeNil())
				Expect(&testconst.TestManagedNotificationWithoutStatus).ToNot(BeNil())
			})
		})
		When("Getting NotificationRecord for which status already exists", func() {
			It("should send service log again after resend window passed when alert is firing", func() {
				gomock.InOrder(
					mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testconst.TestManagedNotification),
					mockClient.EXPECT().Status().Return(mockStatusWriter),
					mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
						func(ctx context.Context, mn *ocmagentv1alpha1.ManagedNotification, client ...client.UpdateOptions) error {
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionAlertFiring).Status).To(Equal(corev1.ConditionTrue))
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionAlertResolved).Status).To(Equal(corev1.ConditionFalse))
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionServiceLogSent).Status).To(Equal(corev1.ConditionTrue))
							return nil
						}),
				)
				_, err := webhookReceiverHandler.updateNotificationStatus(&testconst.TestNotification, &testconst.TestManagedNotification, true)
				Expect(err).Should(BeNil())
			})
			It("should send service log for alert resolved when no longer firing", func() {
				gomock.InOrder(
					mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testconst.TestManagedNotification),
					mockClient.EXPECT().Status().Return(mockStatusWriter),
					mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
						func(ctx context.Context, mn *ocmagentv1alpha1.ManagedNotification, client ...client.UpdateOptions) error {
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionAlertFiring).Status).To(Equal(corev1.ConditionFalse))
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionAlertResolved).Status).To(Equal(corev1.ConditionTrue))
							Expect(mn.Status.NotificationRecords[0].Conditions.GetCondition(ocmagentv1alpha1.ConditionServiceLogSent).Status).To(Equal(corev1.ConditionTrue))
							return nil
						}),
				)
				_, err := webhookReceiverHandler.updateNotificationStatus(&testconst.TestNotification, &testconst.TestManagedNotification, false)
				Expect(err).Should(BeNil())
			})
		})
		It("Update ManagedNotificationStatus without any error", func() {
			gomock.InOrder(
				mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).SetArg(2, testconst.TestManagedNotification),
				mockClient.EXPECT().Status().Return(mockStatusWriter),
				mockStatusWriter.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
			)
			_, err := webhookReceiverHandler.updateNotificationStatus(&testconst.TestNotification, &testconst.TestManagedNotification, true)
			Expect(err).Should(BeNil())
		})
	})

	Context("Checking the response from OCM", func() {
		var testOperationId = "test"
		var testResponseBody = "{\"reason\": \"test\"}"

		It("will treat 201 as a successful response", func() {
			err := responseChecker(testOperationId, http.StatusCreated, []byte(testResponseBody))
			Expect(err).To(BeNil())
		})
		It("will treat all other responses as failures", func() {
			var testFailedResponseCodes = []int{
				http.StatusForbidden,
				http.StatusBadRequest,
				http.StatusUnauthorized,
				http.StatusInternalServerError,
				http.StatusOK,
			}
			for _, code := range testFailedResponseCodes {
				err := responseChecker(testOperationId, code, []byte(testResponseBody))
				Expect(err).NotTo(BeNil())
			}
		})
	})
})
