package store

import "context"

type recordedCorrectionPin struct {
	ref     string
	version int64
	digest  string
}

type recordedCorrectionRoute struct {
	target string
	steps  []string
}

// Historical targets are exact-pin data. Only recorded correction folds read
// this table; live admission and live effects use the declared route reader.
var recordedCorrectionRoutes = map[recordedCorrectionPin]recordedCorrectionRoute{
	{"workflow.break_fix", 1, "sha256:aefce865f350345dc41fc1e2e988e7d5e246fa7fd560335399cf8c826e4cc35a"}:       {"repair", []string{"complete", "verify"}},
	{"workflow.break_fix", 2, "sha256:d7f8d8cc8b951e74751ddafe95c7b9c9d65e606cd73c41b2ceadd5fa2cdf29cb"}:       {"repair", []string{"complete", "verify"}},
	{"workflow.break_fix", 3, "sha256:3a406e35712a33dcab245ab93c77d51e0811ef51fbefb1a54c2fb40ae6b1f8b6"}:       {"repair", []string{"complete", "verify"}},
	{"workflow.break_fix", 4, "sha256:db759043452bf078754a547f4222d1adcc43b8b386af79cf81dfb517c39c4056"}:       {"repair", []string{"complete", "verify"}},
	{"workflow.break_fix", 5, "sha256:bb565fdcb7bcf4b4ba630ea1c56a2d34ac79ab17c486df63648775212b9047e1"}:       {"repair", []string{"complete", "verify"}},
	{"workflow.break_fix", 6, "sha256:ba1d605ecaa729f6ac729f6fc06733404875e2c45c90061c8d6c01b5e785eebc"}:       {"repair", []string{"complete", "verify"}},
	{"workflow.break_fix", 7, "sha256:7ba7c722550e96e45f98ffd6de41cf6fc8d14baca3d400abe9f8a68b700bc667"}:       {"repair", []string{"complete", "refine", "verify"}},
	{"workflow.break_fix", 8, "sha256:91a04421b1935cfb1d5600eda9a55b7c3791cc25527ba1007416d57ebae74368"}:       {"repair", []string{"complete", "refine", "verify"}},
	{"workflow.break_fix", 9, "sha256:d2e5be89e2f0b15e152709915c56f104d89a511aeadeb34b245bcc1f5004c177"}:       {"repair", []string{"complete", "refine", "verify"}},
	{"workflow.break_fix", 10, "sha256:dad5a4de999f2a2f946702fe3f7aee012acda8fdc283d0a123f50662d6cf310d"}:      {"repair", []string{"complete", "refine", "verify"}},
	{"workflow.break_fix", 11, "sha256:2c13955de3099ab12072d1cc450f2fe37a99027903facc11690ad1ea42d2d7a8"}:      {"repair", []string{"complete", "refine", "verify"}},
	{"workflow.break_fix", 12, "sha256:ad0d90e5e531160fea75094a38dcbac00b4ca33bafdc346ea02ccfe2fe44f7d2"}:      {"repair", []string{"complete", "refine", "verify"}},
	{"workflow.break_fix", 13, "sha256:c11469369d6aa689997e0f3c801938fd05ee47d7b1eac37e46de91e26eba1573"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.break_fix", 14, "sha256:c89bc73749f03062931776ffb4ba4cbdcbaae023cf0b9d8be3dd841885dbef36"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.break_fix", 15, "sha256:dc4b081253f694fdd4383792b762b0b03517fa84256f04219c0a6fa15156162c"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.break_fix", 16, "sha256:0f9377a09d89983b2d1769d5232eb27a23d8ea5f51d0745ff0a1675275256b37"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.break_fix", 17, "sha256:c8e15e64059f7cab5b619302ab8b398cd531974cbe56f2c36bc96d76d662c4fb"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.break_fix", 18, "sha256:9b355f4af052d0f48c71d0616177f8b0c2b0cd56bfdfda57a92ea8af46cfaad1"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.break_fix", 19, "sha256:df5d441762daf131a1ae62c1abc9897f0269ebd3890322fff6c9a480ca769cc7"}:      {"repair", []string{"complete", "delivery", "refine", "verify"}},
	{"workflow.implementation", 1, "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 2, "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 3, "sha256:12ecaeb8b7947387905b0354f131308586635e61a553eba25deb1564179cbcd4"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 4, "sha256:454a0d11d32f0ca415a42306da65e0696b5884ee56a6ecbfba7771e5a8dc96ff"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 5, "sha256:64714294b727772997db3eb96e696b7c2b9cae64a01edc840e4fd9a9d5226661"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 6, "sha256:6f512e8f25772ef071d00f1378a8f675cac2624fe230c7c75a1e82635e2de785"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 7, "sha256:7b0e0582da3716e7d44a8097d16afa81deecf59b61d3af4c02833fa6f443291a"}:  {"execution", []string{"acceptance", "release"}},
	{"workflow.implementation", 8, "sha256:15028d1f2b1d00df2ea0c0649f72dc9fb6151fb05bc88e3a6002eb5a93568f7c"}:  {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 9, "sha256:787ae3f02df30630408ef1c12badcd280eb604e71909fe7227b3c08b94cb71ca"}:  {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 10, "sha256:9335bd4830c5592240e578528582a26b51702f3f6d0b4f20916c47267fd160ac"}: {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 11, "sha256:8a205e8b43f537caa650e72c667051017d9b26f77f675d1f5c14511743ef6462"}: {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 12, "sha256:26340249dcc709a625db60711bc980acb2c8bdfb8be1588b1b1b80839b8d6f33"}: {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 13, "sha256:a17d5afc3ee430aeaf406a004e93489daafea7138eb178c8e0ae63506496d341"}: {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 14, "sha256:609beef3fda073435d22fcba81e9661b64979c77561d485e6c60268a243056cf"}: {"execution", []string{"acceptance", "refine", "release"}},
	{"workflow.implementation", 15, "sha256:e67484ab82967317470b819b1efe8c0af71448fa8a6c1eb1dd0b2989a4b5f996"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 16, "sha256:ac13f85d1e460c3f54be49e3550aeac564e510ed66db524ee10f619bd2ea05cc"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 17, "sha256:a50776df39e5a2a6c961b2a580bf320675eacf15d2d1fa0a9d9723d3911cde80"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 18, "sha256:0f114bc254d375e97f6c8bf702ce89550117e0d7d5b7c5af9a33927f435437db"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 19, "sha256:60bbb73777f24c3367c88b7fcdfb44cdd3e15b1a7ff6dd385ead60f522d38cb5"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 20, "sha256:eb99ac7107790b9572b827481133ddcea50bd14085b2719e7b4e670910ac928c"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 21, "sha256:0a6ed9d4f03954f8d8802b4c953cbec7338860f3552aa83bac4b97fcf469c901"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
	{"workflow.implementation", 22, "sha256:1bef12c2d072dae7bfcc65a2b097248678770481cc849bf79107cc9833b3b7f4"}: {"execution", []string{"acceptance", "delivery", "refine", "release"}},
}

func workflowRecordedCorrectionReturnTarget(entry RegisteredDefinition, step string) (string, error) {
	definition := entry.Definition
	if err := BuiltinWorkflowRegistry().Verify(definition.Ref, definition.Version, entry.Digest); err != nil {
		return "", err
	}
	pin := recordedCorrectionPin{definition.Ref, definition.Version, entry.Digest}
	if route, ok := recordedCorrectionRoutes[pin]; ok && containsString(route.steps, step) {
		return route.target, nil
	}
	return workflowCorrectionReturnTarget(definition, step), nil
}

// workflowFoldCorrectionReturnTarget owns the fold's whole correction-return
// selection and its checks. Live effects read only the declared route reader;
// replay reads the verified historical table, which may name a target the
// current definition no longer declares.
func workflowFoldCorrectionReturnTarget(ctx context.Context, entry RegisteredDefinition, recordedStep, currentStep string) (string, error) {
	var target string
	var err error
	if isWorkflowReplay(ctx) {
		if recordedStep != currentStep {
			return "", newFailure(KindIllegalLifecycleTransition, "fold_event", "recorded correction source step differs from the pinned current step", false, "restore a consistent recorded workflow history")
		}
		target, err = workflowRecordedCorrectionReturnTarget(entry, currentStep)
	} else {
		target = workflowCorrectionReturnTarget(entry.Definition, currentStep)
	}
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", newFailure(KindIllegalLifecycleTransition, "fold_event", "correction has no declared recovery route to a producer step", false, "repair the pinned workflow definition")
	}
	return target, nil
}
