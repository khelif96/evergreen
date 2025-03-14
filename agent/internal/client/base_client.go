package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/agent/internal/redactor"
	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/cloud"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/artifact"
	"github.com/evergreen-ci/evergreen/model/manifest"
	patchmodel "github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/evergreen/model/testlog"
	restmodel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/evergreen/taskoutput"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/evergreen-ci/juniper/gopb"
	"github.com/evergreen-ci/timber"
	"github.com/evergreen-ci/utility"
	"github.com/google/go-github/v52/github"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/level"
	"github.com/mongodb/grip/logging"
	"github.com/mongodb/grip/message"
	"github.com/mongodb/grip/send"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

// baseCommunicator provides common methods for Communicator functionality but
// does not implement the entire interface.
type baseCommunicator struct {
	serverURL       string
	retry           utility.RetryOptions
	httpClient      *http.Client
	reqHeaders      map[string]string
	cedarGRPCClient *grpc.ClientConn

	lastMessageSent time.Time
	mutex           sync.RWMutex
}

func newBaseCommunicator(serverURL string, reqHeaders map[string]string) baseCommunicator {
	return baseCommunicator{
		retry: utility.RetryOptions{
			MaxAttempts: defaultMaxAttempts,
			MinDelay:    defaultTimeoutStart,
			MaxDelay:    defaultTimeoutMax,
		},
		serverURL:  serverURL,
		reqHeaders: reqHeaders,
	}
}

// Close cleans up the resources being used by the communicator.
func (c *baseCommunicator) Close() {
	if c.httpClient != nil {
		utility.PutHTTPClient(c.httpClient)
	}
}

// SetTimeoutStart sets the initial timeout for a request.
func (c *baseCommunicator) SetTimeoutStart(timeoutStart time.Duration) {
	c.retry.MinDelay = timeoutStart
}

// SetTimeoutMax sets the maximum timeout for a request.
func (c *baseCommunicator) SetTimeoutMax(timeoutMax time.Duration) {
	c.retry.MaxDelay = timeoutMax
}

// SetMaxAttempts sets the number of attempts a request will be made.
func (c *baseCommunicator) SetMaxAttempts(attempts int) {
	c.retry.MaxAttempts = attempts
}

func (c *baseCommunicator) UpdateLastMessageTime() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.lastMessageSent = time.Now()
}

func (c *baseCommunicator) LastMessageAt() time.Time {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.lastMessageSent
}

func (c *baseCommunicator) resetClient() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.httpClient != nil {
		utility.PutHTTPClient(c.httpClient)
	}

	c.httpClient = utility.GetDefaultHTTPRetryableClient()
	c.httpClient.Timeout = heartbeatTimeout
}

func (c *baseCommunicator) createCedarGRPCConn(ctx context.Context) error {
	if c.cedarGRPCClient == nil {
		cc, err := c.GetCedarConfig(ctx)
		if err != nil {
			return errors.Wrap(err, "getting Cedar config")
		}

		if cc.GRPCBaseURL == "" && cc.BaseURL == "" {
			// No cedar URLs probably means we are running
			// evergreen locally or in some testing mode.
			return nil
		} else if cc.GRPCBaseURL == "" {
			// Default the GRPC url to the HTTP base url if it's unpopulated
			cc.GRPCBaseURL = cc.BaseURL
		}

		dialOpts := timber.DialCedarOptions{
			BaseAddress: cc.GRPCBaseURL,
			RPCPort:     cc.RPCPort,
			Username:    cc.Username,
			APIKey:      cc.APIKey,
			// Insecure should always be set to false except when
			// running Cedar locally, e.g. with our smoke tests.
			Insecure: cc.Insecure,
			Retries:  10,
		}
		c.cedarGRPCClient, err = timber.DialCedar(ctx, c.httpClient, dialOpts)
		if err != nil {
			return errors.Wrap(err, "creating Cedar gRPC client connection")
		}
	}

	// We should always check the health of the conn as a sanity check,
	// this way we can fail the agent early and avoid task system failures.
	healthClient := gopb.NewHealthClient(c.cedarGRPCClient)
	_, err := healthClient.Check(ctx, &gopb.HealthCheckRequest{})
	return errors.Wrap(err, "checking Cedar gRPC health")
}

// GetProjectRef loads the task's project.
func (c *baseCommunicator) GetProjectRef(ctx context.Context, taskData TaskData) (*model.ProjectRef, error) {
	projectRef := &model.ProjectRef{}
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("project_ref")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting project ref").Error())
	}
	if err = utility.ReadJSON(resp.Body, projectRef); err != nil {
		return nil, errors.Wrap(err, "reading project ref from response")
	}
	return projectRef, nil
}

// DisableHost signals to the app server that the host should be disabled.
func (c *baseCommunicator) DisableHost(ctx context.Context, hostID string, details apimodels.DisableInfo) error {
	info := requestInfo{
		method: http.MethodPost,
		path:   fmt.Sprintf("hosts/%s/disable", hostID),
	}
	resp, err := c.retryRequest(ctx, info, &details)
	if err != nil {
		return util.RespError(resp, errors.Wrapf(err, "disabling host '%s'", hostID).Error())
	}

	defer resp.Body.Close()
	return nil
}

// GetTask returns the active task.
func (c *baseCommunicator) GetTask(ctx context.Context, taskData TaskData) (*task.Task, error) {
	task := &task.Task{}
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting task info").Error())
	}
	if err = utility.ReadJSON(resp.Body, task); err != nil {
		return nil, errors.Wrap(err, "reading task info from response")
	}
	return task, nil
}

// GetDisplayTaskInfoFromExecution returns the display task info associated
// with the execution task.
func (c *baseCommunicator) GetDisplayTaskInfoFromExecution(ctx context.Context, td TaskData) (*apimodels.DisplayTaskInfo, error) {
	info := requestInfo{
		method:   http.MethodGet,
		path:     fmt.Sprintf("task/%s/display_task", td.ID),
		taskData: &td,
	}
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting parent display task info").Error())
	}

	displayTaskInfo := &apimodels.DisplayTaskInfo{}
	err = utility.ReadJSON(resp.Body, &displayTaskInfo)
	if err != nil {
		return nil, errors.Wrap(err, "reading parent display task info from response")
	}

	return displayTaskInfo, nil
}

func (c *baseCommunicator) GetDistroView(ctx context.Context, taskData TaskData) (*apimodels.DistroView, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("distro_view")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting distro view").Error())
	}
	var dv apimodels.DistroView
	if err = utility.ReadJSON(resp.Body, &dv); err != nil {
		return nil, errors.Wrap(err, "reading distro view from response")
	}
	return &dv, nil
}

func (c *baseCommunicator) GetHostView(ctx context.Context, taskData TaskData) (*apimodels.HostView, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("host_view")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting host view").Error())
	}
	var hv apimodels.HostView
	if err = utility.ReadJSON(resp.Body, &hv); err != nil {
		return nil, errors.Wrap(err, "reading host view from response")
	}
	return &hv, nil
}

// GetDistroAMI returns the distro for the task.
func (c *baseCommunicator) GetDistroAMI(ctx context.Context, distro, region string, taskData TaskData) (string, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.path = fmt.Sprintf("distros/%s/ami", distro)
	if region != "" {
		info.path = fmt.Sprintf("%s?region=%s", info.path, region)
	}
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return "", util.RespError(resp, errors.Wrap(err, "getting distro AMI").Error())
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "reading distro AMI from response")
	}
	return string(out), nil
}

func (c *baseCommunicator) GetProject(ctx context.Context, taskData TaskData) (*model.Project, error) {
	info := requestInfo{
		method:             http.MethodGet,
		taskData:           &taskData,
		retryOnInvalidBody: true, // This route has returned an invalid body for older distros. See DEVPROD-7885.
	}
	info.setTaskPathSuffix("parser_project")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting parser project").Error())
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "reading parser project from response")
	}

	return model.GetProjectFromBSON(respBytes)
}

func (c *baseCommunicator) GetExpansions(ctx context.Context, taskData TaskData) (util.Expansions, error) {
	e := util.Expansions{}
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("expansions")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting expansions").Error())
	}

	err = utility.ReadJSON(resp.Body, &e)
	if err != nil {
		return nil, errors.Wrap(err, "reading expansions from response")
	}
	return e, nil
}

func (c *baseCommunicator) GetExpansionsAndVars(ctx context.Context, taskData TaskData) (*apimodels.ExpansionsAndVars, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("expansions_and_vars")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting expansions and vars").Error())
	}

	var expAndVars apimodels.ExpansionsAndVars
	if err = utility.ReadJSON(resp.Body, &expAndVars); err != nil {
		return nil, errors.Wrap(err, "reading expansions and vars from response")
	}
	return &expAndVars, nil
}

func (c *baseCommunicator) Heartbeat(ctx context.Context, taskData TaskData) (string, error) {
	data := any("heartbeat")
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("heartbeat")
	resp, err := c.request(ctx, info, data)
	if err != nil {
		return "", errors.Wrap(err, "sending heartbeat")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		// The task has an incorrect task secret because it was aborted and
		// restarted to a new execution (which gets a new secret).
		return evergreen.TaskFailed, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", util.RespError(resp, "sending heartbeat")
	}

	heartbeatResponse := &apimodels.HeartbeatResponse{}
	if err = utility.ReadJSON(resp.Body, heartbeatResponse); err != nil {
		return "", errors.Wrap(err, "reading heartbeat reply from response")
	}
	if heartbeatResponse.Abort {
		// The task has been aborted, but not restarted to a new execution.
		return evergreen.TaskFailed, nil
	}
	return "", nil
}

// GetCedarGRPCConn returns the client connection to cedar if it exists, or
// creates it if it doesn't exist.
func (c *baseCommunicator) GetCedarGRPCConn(ctx context.Context) (*grpc.ClientConn, error) {
	if err := c.createCedarGRPCConn(ctx); err != nil {
		return nil, errors.Wrap(err, "setting up Cedar gRPC connection")
	}
	return c.cedarGRPCClient, nil
}

func (c *baseCommunicator) GetLoggerProducer(ctx context.Context, tsk *task.Task, config *LoggerConfig) (LoggerProducer, error) {
	if config == nil {
		config = &LoggerConfig{
			Agent:  []LogOpts{{Sender: model.EvergreenLogSender}},
			System: []LogOpts{{Sender: model.EvergreenLogSender}},
			Task:   []LogOpts{{Sender: model.EvergreenLogSender}},
		}
	}

	exec, err := c.makeSender(ctx, tsk, config, taskoutput.TaskLogTypeAgent)
	if err != nil {
		return nil, errors.Wrap(err, "making agent logger")
	}
	task, err := c.makeSender(ctx, tsk, config, taskoutput.TaskLogTypeTask)
	if err != nil {
		return nil, errors.Wrap(err, "making task logger")
	}
	system, err := c.makeSender(ctx, tsk, config, taskoutput.TaskLogTypeSystem)
	if err != nil {
		return nil, errors.Wrap(err, "making system logger")
	}

	return &logHarness{
		execution: logging.MakeGrip(exec),
		task:      logging.MakeGrip(task),
		system:    logging.MakeGrip(system),
	}, nil
}

func (c *baseCommunicator) makeSender(ctx context.Context, tsk *task.Task, config *LoggerConfig, logType taskoutput.TaskLogType) (send.Sender, error) {
	levelInfo := send.LevelInfo{Default: level.Info, Threshold: level.Debug}
	var senders []send.Sender
	if config.SendToGlobalSender {
		senders = append(senders, redactor.NewRedactingSender(grip.GetSender(), config.RedactorOpts))
	}

	var sender send.Sender
	var err error

	taskOpts := taskoutput.TaskOptions{
		ProjectID: tsk.Project,
		TaskID:    tsk.Id,
		Execution: tsk.Execution,
	}
	senderOpts := taskoutput.EvergreenSenderOptions{
		LevelInfo:     levelInfo,
		FlushInterval: time.Minute,
	}
	sender, err = tsk.TaskOutputInfo.TaskLogs.NewSender(ctx, taskOpts, senderOpts, logType)
	if err != nil {
		return nil, errors.Wrap(err, "creating Evergreen task log sender")
	}

	sender = redactor.NewRedactingSender(sender, config.RedactorOpts)
	if logType == taskoutput.TaskLogTypeTask {
		sender = makeTimeoutLogSender(sender, c)
	}
	senders = append(senders, sender)
	return send.NewConfiguredMultiSender(senders...), nil
}

// GetTaskPatch tries to get the patch data from the server in json format,
// and unmarhals it into a patch struct. The GET request is attempted
// multiple times upon failure. If patchId is not specified, the task's
// patch is returned.
func (c *baseCommunicator) GetTaskPatch(ctx context.Context, taskData TaskData, patchId string) (*patchmodel.Patch, error) {
	patch := patchmodel.Patch{}
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	suffix := "patch"
	if patchId != "" {
		suffix = fmt.Sprintf("%s?patch=%s", suffix, patchId)
	}
	info.setTaskPathSuffix(suffix)
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrapf(err, "getting patch '%s' for task", patchId).Error())
	}

	if err = utility.ReadJSON(resp.Body, &patch); err != nil {
		return nil, errors.Wrap(err, "reading patch for task from response")
	}

	return &patch, nil
}

// GetTaskVersion tries to get the patch data from the server in json format,
// and unmarhals it into a version struct. The GET request is attempted
// multiple times upon failure. The route can only retrieve the calling task's version.
func (c *baseCommunicator) GetTaskVersion(ctx context.Context, taskData TaskData) (*model.Version, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	suffix := "version"
	info.setTaskPathSuffix(suffix)
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting version for task").Error())
	}

	version := model.Version{}
	if err = utility.ReadJSON(resp.Body, &version); err != nil {
		return nil, errors.Wrap(err, "reading version for task from response")
	}

	return &version, nil
}

// GetCedarConfig returns the Cedar service configuration.
func (c *baseCommunicator) GetCedarConfig(ctx context.Context) (*apimodels.CedarConfig, error) {
	info := requestInfo{
		method: http.MethodGet,
		path:   "agent/cedar_config",
	}

	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting the Cedar config").Error())
	}

	config := &apimodels.CedarConfig{}
	if err := utility.ReadJSON(resp.Body, config); err != nil {
		return nil, errors.Wrap(err, "reading the Cedar config from response")
	}

	return config, nil
}

func (c *baseCommunicator) GetAgentSetupData(ctx context.Context) (*apimodels.AgentSetupData, error) {
	info := requestInfo{
		method: http.MethodGet,
		path:   "agent/setup",
	}

	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting agent setup data").Error())
	}

	var data apimodels.AgentSetupData
	if err := utility.ReadJSON(resp.Body, &data); err != nil {
		return nil, errors.Wrap(err, "reading agent setup data from response")
	}

	return &data, nil
}

// GetPatchFiles is used by the git.get_project plugin and fetches
// patches from the database, used in patch builds.
func (c *baseCommunicator) GetPatchFile(ctx context.Context, taskData TaskData, patchFileID string) (string, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("git/patchfile/" + patchFileID)
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return "", util.RespError(resp, errors.Wrapf(err, "getting patch file '%s'", patchFileID).Error())
	}
	defer resp.Body.Close()

	var result []byte
	result, err = io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrapf(err, "reading patch file '%s' from response", patchFileID)
	}

	return string(result), nil
}

// SendTestLog is used by the attach plugin to add to the test_logs
// collection for log data associated with a test.
func (c *baseCommunicator) SendTestLog(ctx context.Context, taskData TaskData, log *testlog.TestLog) (string, error) {
	if log == nil {
		return "", nil
	}

	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("test_logs")
	resp, err := c.retryRequest(ctx, info, log)
	if err != nil {
		return "", util.RespError(resp, errors.Wrap(err, "sending test log").Error())
	}

	logReply := struct {
		ID string `json:"_id"`
	}{}
	if err = utility.ReadJSON(resp.Body, &logReply); err != nil {
		return "", errors.Wrap(err, "reading test log reply from response")
	}
	logID := logReply.ID

	return logID, nil
}

func (c *baseCommunicator) SetResultsInfo(ctx context.Context, taskData TaskData, service string, failed bool) error {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}
	info.path = fmt.Sprintf("task/%s/set_results_info", taskData.ID)
	resp, err := c.retryRequest(ctx, info, &apimodels.TaskTestResultsInfo{Service: service, Failed: failed})
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "setting results info").Error())
	}
	defer resp.Body.Close()

	return nil
}

func (c *baseCommunicator) NewPush(ctx context.Context, taskData TaskData, req *apimodels.S3CopyRequest) (*model.PushLog, error) {
	newPushLog := model.PushLog{}
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}

	info.setTaskPathSuffix("new_push")
	resp, err := c.retryRequest(ctx, info, req)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "adding push log").Error())
	}

	if err = utility.ReadJSON(resp.Body, &newPushLog); err != nil {
		return nil, errors.Wrap(err, "reading push log reply from response")
	}

	return &newPushLog, nil
}

func (c *baseCommunicator) UpdatePushStatus(ctx context.Context, taskData TaskData, pushLog *model.PushLog) error {
	newPushLog := model.PushLog{}
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}

	info.setTaskPathSuffix("update_push_status")
	resp, err := c.retryRequest(ctx, info, pushLog)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "updating push log status").Error())
	}

	if err = utility.ReadJSON(resp.Body, &newPushLog); err != nil {
		return errors.Wrap(err, "reading push log reply from response")
	}

	return nil
}

// AttachFiles attaches task files.
func (c *baseCommunicator) AttachFiles(ctx context.Context, taskData TaskData, taskFiles []*artifact.File) error {
	if len(taskFiles) == 0 {
		return nil
	}

	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("files")
	resp, err := c.retryRequest(ctx, info, taskFiles)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "posting files").Error())
	}
	defer resp.Body.Close()

	return nil
}

func (c *baseCommunicator) SetDownstreamParams(ctx context.Context, downstreamParams []patchmodel.Parameter, taskData TaskData) error {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}

	info.setTaskPathSuffix("downstreamParams")
	resp, err := c.retryRequest(ctx, info, downstreamParams)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "setting downstream params").Error())
	}
	defer resp.Body.Close()

	return nil
}

func (c *baseCommunicator) GetManifest(ctx context.Context, taskData TaskData) (*manifest.Manifest, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("manifest/load")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "loading manifest").Error())
	}

	mfest := manifest.Manifest{}
	if err = utility.ReadJSON(resp.Body, &mfest); err != nil {
		return nil, errors.Wrap(err, "reading manifest from response")
	}

	return &mfest, nil
}

func (c *baseCommunicator) KeyValInc(ctx context.Context, taskData TaskData, kv *model.KeyVal) error {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("keyval/inc")
	resp, err := c.retryRequest(ctx, info, kv.Key)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "incrementing key").Error())
	}

	if err = utility.ReadJSON(resp.Body, kv); err != nil {
		return errors.Wrap(err, "reading key-value reply from response")
	}

	return nil
}

// GenerateTasks posts new tasks for the `generate.tasks` command.
func (c *baseCommunicator) GenerateTasks(ctx context.Context, td TaskData, jsonBytes []json.RawMessage) error {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &td,
		// When generated tasks are large and evergreen is under load, we may not be able to ingest the
		// data fast enough leading to a buffer overflow and a 413 status code. Therefore, a 413 status
		// code in this case is transitive and we should retry.
		retryOn413: true,
	}
	info.path = fmt.Sprintf("task/%s/generate", td.ID)
	resp, err := c.retryRequest(ctx, info, jsonBytes)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "sending generate.tasks request").Error())
	}
	defer resp.Body.Close()

	return nil
}

// GenerateTasksPoll posts new tasks for the `generate.tasks` command.
func (c *baseCommunicator) GenerateTasksPoll(ctx context.Context, td TaskData) (*apimodels.GeneratePollResponse, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &td,
	}
	info.path = fmt.Sprintf("task/%s/generate", td.ID)
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "sending generate.tasks poll request").Error())
	}
	generated := &apimodels.GeneratePollResponse{}
	if err := utility.ReadJSON(resp.Body, generated); err != nil {
		return nil, errors.Wrap(err, "reading generate.tasks poll reply from response")
	}
	return generated, nil
}

// CreateHost requests a new host be created
func (c *baseCommunicator) CreateHost(ctx context.Context, td TaskData, options apimodels.CreateHost) ([]string, error) {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &td,
	}
	info.path = fmt.Sprintf("hosts/%s/create", td.ID)
	resp, err := c.retryRequest(ctx, info, options)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "sending host.create request").Error())
	}

	ids := []string{}
	if err = utility.ReadJSON(resp.Body, &ids); err != nil {
		return nil, errors.Wrap(err, "reading host IDs from response")
	}
	return ids, nil
}

func (c *baseCommunicator) ListHosts(ctx context.Context, td TaskData) (restmodel.HostListResults, error) {
	info := requestInfo{
		method:   http.MethodGet,
		taskData: &td,
		path:     fmt.Sprintf("hosts/%s/list", td.ID),
	}

	result := restmodel.HostListResults{}
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return result, util.RespError(resp, errors.Wrap(err, "listing hosts").Error())
	}

	if err := utility.ReadJSON(resp.Body, &result); err != nil {
		return result, errors.Wrap(err, "reading hosts from response")
	}
	return result, nil
}

func (c *baseCommunicator) GetDistroByName(ctx context.Context, id string) (*restmodel.APIDistro, error) {
	info := requestInfo{
		method: http.MethodGet,
		path:   fmt.Sprintf("distros/%s", id),
	}

	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrapf(err, "getting distro '%s'", id).Error())
	}

	d := &restmodel.APIDistro{}
	if err = utility.ReadJSON(resp.Body, &d); err != nil {
		return nil, errors.Wrapf(err, "reading distro '%s' from response", id)
	}

	return d, nil

}

// StartTask marks the task as started.
func (c *baseCommunicator) StartTask(ctx context.Context, taskData TaskData) error {
	grip.Info(message.Fields{
		"message": "started StartTask",
		"task_id": taskData.ID,
	})
	pidStr := strconv.Itoa(os.Getpid())
	taskStartRequest := &apimodels.TaskStartRequest{Pid: pidStr}
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &taskData,
	}
	info.setTaskPathSuffix("start")
	resp, err := c.retryRequest(ctx, info, taskStartRequest)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "starting task").Error())
	}
	defer resp.Body.Close()
	grip.Info(message.Fields{
		"message": "finished StartTask",
		"task_id": taskData.ID,
	})
	return nil
}

// GetDockerStatus returns status of the container for the given host
func (c *baseCommunicator) GetDockerStatus(ctx context.Context, hostID string) (*cloud.ContainerStatus, error) {
	info := requestInfo{
		method: http.MethodGet,
		path:   fmt.Sprintf("hosts/%s/status", hostID),
	}
	resp, err := c.request(ctx, info, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "getting status for container '%s'", hostID)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, util.RespErrorf(resp, "getting status for container '%s'", hostID)
	}
	status := cloud.ContainerStatus{}
	if err := utility.ReadJSON(resp.Body, &status); err != nil {
		return nil, errors.Wrapf(err, "reading container status from response for container '%s'", hostID)
	}

	return &status, nil
}

func (c *baseCommunicator) GetDockerLogs(ctx context.Context, hostID string, startTime time.Time, endTime time.Time, isError bool) ([]byte, error) {
	path := fmt.Sprintf("/hosts/%s/logs", hostID)
	if isError {
		path = fmt.Sprintf("%s/error", path)
	} else {
		path = fmt.Sprintf("%s/output", path)
	}
	if !utility.IsZeroTime(startTime) && !utility.IsZeroTime(endTime) {
		path = fmt.Sprintf("%s?start_time=%s&end_time=%s", path, startTime.Format(time.RFC3339), endTime.Format(time.RFC3339))
	} else if !utility.IsZeroTime(startTime) {
		path = fmt.Sprintf("%s?start_time=%s", path, startTime.Format(time.RFC3339))
	} else if !utility.IsZeroTime(endTime) {
		path = fmt.Sprintf("%s?end_time=%s", path, endTime.Format(time.RFC3339))
	}

	info := requestInfo{
		method: http.MethodGet,
		path:   path,
	}
	resp, err := c.request(ctx, info, "")
	if err != nil {
		return nil, errors.Wrapf(err, "getting logs for container '%s'", hostID)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, util.RespErrorf(resp, "getting logs for container '%s'", hostID)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "reading logs from response")
	}

	return body, nil
}

func (c *baseCommunicator) ConcludeMerge(ctx context.Context, patchId, status string, td TaskData) error {
	info := requestInfo{
		method:   http.MethodPost,
		path:     fmt.Sprintf("commit_queue/%s/conclude_merge", patchId),
		taskData: &td,
	}
	body := struct {
		Status string `json:"status"`
	}{
		Status: status,
	}
	resp, err := c.request(ctx, info, body)
	if err != nil {
		return errors.Wrapf(err, "concluding merge for patch '%s'", patchId)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return util.RespErrorf(resp, "concluding merge for patch '%s'", patchId)
	}

	return nil
}

func (c *baseCommunicator) GetAdditionalPatches(ctx context.Context, patchId string, td TaskData) ([]string, error) {
	info := requestInfo{
		method:   http.MethodGet,
		path:     fmt.Sprintf("commit_queue/%s/additional", patchId),
		taskData: &td,
	}
	resp, err := c.request(ctx, info, nil)
	if err != nil {
		return nil, errors.Wrap(err, "getting additional patches")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, util.RespError(resp, "getting additional patches")
	}
	patches := []string{}
	if err := utility.ReadJSON(resp.Body, &patches); err != nil {
		return nil, errors.Wrap(err, "reading patch IDs from response")
	}

	return patches, nil
}

func (c *baseCommunicator) CreateInstallationTokenForClone(ctx context.Context, td TaskData, owner, repo string) (string, error) {
	info := requestInfo{
		method:   http.MethodGet,
		path:     fmt.Sprintf("task/%s/installation_token/%s/%s", td.ID, owner, repo),
		taskData: &td,
	}
	resp, err := c.request(ctx, info, nil)
	if err != nil {
		return "", errors.Wrapf(err, "creating installation token to clone '%s/%s'", owner, repo)
	}

	if resp.StatusCode != http.StatusOK {
		return "", util.RespErrorf(resp, "creating installation token to clone '%s/%s'", owner, repo)
	}
	token := apimodels.Token{}
	if err := utility.ReadJSON(resp.Body, &token); err != nil {
		return "", errors.Wrap(err, "reading token from response")
	}

	return token.Token, nil
}

func (c *baseCommunicator) CreateGitHubDynamicAccessToken(ctx context.Context, td TaskData, owner, repo string, permissions *github.InstallationPermissions) (string, *github.InstallationPermissions, error) {
	info := requestInfo{
		method:   http.MethodPost,
		path:     fmt.Sprintf("task/%s/github_dynamic_access_token/%s/%s", td.ID, owner, repo),
		taskData: &td,
	}
	resp, err := c.request(ctx, info, permissions)
	if err != nil {
		return "", nil, errors.Wrapf(err, "creating github dynamic access token for '%s/%s'", owner, repo)
	}

	if resp.StatusCode != http.StatusOK {
		return "", nil, util.RespErrorf(resp, "creating github dynamic access token for '%s/%s'", owner, repo)
	}
	r := apimodels.Token{}
	if err := utility.ReadJSON(resp.Body, &r); err != nil {
		return "", nil, errors.Wrap(err, "reading github dynamic access token from response")
	}

	return r.Token, r.Permissions, nil
}

func (c *baseCommunicator) RevokeGitHubDynamicAccessToken(ctx context.Context, td TaskData, token string) error {
	info := requestInfo{
		method:   http.MethodDelete,
		path:     fmt.Sprintf("task/%s/github_dynamic_access_token", td.ID),
		taskData: &td,
	}
	resp, err := c.request(ctx, info, apimodels.Token{Token: token})
	if err != nil {
		return errors.Wrap(err, "revoking github dynamic access token")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return util.RespError(resp, "revoking github dynamic access token")
	}
	return nil
}

// MarkFailedTaskToRestart will mark the task to automatically restart upon completion
func (c *baseCommunicator) MarkFailedTaskToRestart(ctx context.Context, td TaskData) error {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &td,
	}
	info.setTaskPathSuffix("restart")
	resp, err := c.retryRequest(ctx, info, nil)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "marking task for restart").Error())
	}
	defer resp.Body.Close()
	return nil
}

// UpsertCheckRun upserts a checkrun for a task
func (c *baseCommunicator) UpsertCheckRun(ctx context.Context, td TaskData, checkRunOutput apimodels.CheckRunOutput) error {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &td,
	}
	info.setTaskPathSuffix("check_run")
	resp, err := c.retryRequest(ctx, info, &checkRunOutput)
	if err != nil {
		return util.RespError(resp, errors.Wrap(err, "upserting checkRun").Error())
	}

	defer resp.Body.Close()
	return nil
}

func (c *baseCommunicator) AssumeRole(ctx context.Context, td TaskData, request apimodels.AssumeRoleRequest) (*apimodels.AWSCredentials, error) {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &td,
	}
	info.setTaskPathSuffix("aws/assume_role")
	resp, err := c.retryRequest(ctx, info, &request)
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "assuming role").Error())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, util.RespError(resp, "trouble assuming role")
	}
	var creds apimodels.AWSCredentials
	if err := utility.ReadJSON(resp.Body, &creds); err != nil {
		return nil, errors.Wrap(err, "reading assume role response")
	}
	return &creds, nil
}

func (c *baseCommunicator) S3Credentials(ctx context.Context, td TaskData, bucket string) (*apimodels.AWSCredentials, error) {
	info := requestInfo{
		method:   http.MethodPost,
		taskData: &td,
	}
	info.setTaskPathSuffix("aws/s3_credentials")
	resp, err := c.retryRequest(ctx, info, apimodels.S3CredentialsRequest{
		Bucket: bucket,
	})
	if err != nil {
		return nil, util.RespError(resp, errors.Wrap(err, "getting s3 credentials").Error())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, util.RespError(resp, "trouble getting s3 credentials")
	}
	var creds apimodels.AWSCredentials
	if err := utility.ReadJSON(resp.Body, &creds); err != nil {
		return nil, errors.Wrap(err, "reading s3 credentials response")
	}
	return &creds, nil
}
