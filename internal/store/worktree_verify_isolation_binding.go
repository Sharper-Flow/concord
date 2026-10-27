package store

func nativeIsolationPreparationMatches(p nativeOraclePlan, o *NativeOraclePreparation) bool {
	if o == nil {
		return false
	}
	if p.Target == nil || p.Target.Recipe.Isolation == "" {
		return o.IsolationPolicy == "" && o.IsolationFingerprint == "" && o.IsolationEnforcement == ""
	}
	e := p.Target.Environment.Isolation
	return p.Target.Recipe.Isolation == nativeIsolationLoopback && e != nil && e.Policy == nativeIsolationLoopback && e.Profile == nativeIsolationProfile && len(e.Files) != 0 && len(e.Exports) != 0 &&
		o.IsolationPolicy == e.Policy && o.IsolationEnforcement == "established" && o.IsolationFingerprint == nativeIsolationEnvironmentDigest(e)
}

func nativeIsolationResultMatches(p nativeOraclePlan, o *NativeOraclePreparation, stages []NativeOracleStage) bool {
	if !nativeIsolationPreparationMatches(p, o) {
		return false
	}
	required := p.Target != nil && p.Target.Recipe.Isolation != ""
	if required && len(stages) == 0 {
		return false
	}
	for _, stage := range stages {
		if required {
			if !stage.IsolationEnforced || !workflowDigest(stage.IsolationFingerprint) {
				return false
			}
		} else if stage.IsolationEnforced || stage.IsolationFingerprint != "" {
			return false
		}
	}
	return true
}
