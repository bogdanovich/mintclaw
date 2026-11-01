package diagnostictrace

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestDecodeUsesNativeRawMessageContract(t *testing.T) {
	trace, err := Finalize(baseTrace(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(trace)
	decoded, err := Decode(data)
	if err != nil || !reflect.DeepEqual(trace, decoded) {
		t.Fatal("native byte decoder changed the trace", err)
	}
	for _, bad := range [][]byte{
		nil, append(append([]byte{}, data...), []byte(`{}`)...),
		[]byte(`{"unknown":"field"}`), make([]byte, HardMaxTraceBytes+1),
	} {
		if _, err = Decode(bad); err == nil {
			t.Fatal("invalid native document decoded")
		}
	}
}
