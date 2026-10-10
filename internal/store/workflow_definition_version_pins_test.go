package store

import (
	"fmt"
	"testing"
)

// workflowDefinitionVersionPins holds the digest of every registered workflow
// definition version (issue #861). A released version's digest may never
// change: instances pin (ref, version, digest), and the registry verifies all
// three on every read and dispatch. CD-0112 mutated the version-1 definitions
// in place, which moved these digests and left 158 live instances wedged.
//
// Version 1 of break_fix, implementation, generic_one_off, and research is
// the content the store's pinned instances were created under, frozen in
// workflow_registry_versions.go. Version 2 is the CD-0112 content. Version 3
// (version 2 for architecture_spike, ops_runbook, and static_analysis, which
// no instance pinned at their pre-join content) composes the lane-step
// dispatch join (#892). Version 4 (version 3 for those three definitions)
// adds the action-specific payload contracts from issue #776. Version 5
// (version 4 for those three definitions) adds record_worker_failure (#869).
// Version 6 adds the CD-0124 hold-only break-fix verify recovery route.
// Version 7 of break_fix and version 8 of implementation add the mandatory
// refinement step and its artifact evidence requirement. Version 8 of
// break_fix and version 9 of implementation add the failure return edge.
// Version 11 of break_fix and version 13 of implementation add the CD-0156
// mandatory alignment step with its single record_alignment advance exit.
// The latest versions add the shared evidence-reference payload contract.
// Version 14 of break_fix and version 16 of implementation add the CD-0166
// gate corrective return; versions 13 and 15 carry the closed three-action
// gate. The current versions compose the CD-0187 checkpoint pair:
// implementation 17, break_fix 15, research 10, architecture_spike 11,
// ops_runbook 12, static_analysis 9, and generic_one_off 10 carry
// dispatch_worker and accept_worker_evidence on their confirmation steps.
// CD-0192 ships implementation 18 and break_fix 16, whose refine
// record_delivery exit demands a green worktree_verify run bound in the
// current refine epoch; the guard the versions activate lives in the
// dispatcher, and the definition content keeps its predecessors' shape.
// The current versions ship the confirm_premise declaration:
// implementation 19, break_fix 17, research 11, architecture_spike 12,
// ops_runbook 13, static_analysis 10, and generic_one_off 11 declare
// selected_choice and decision_context_digest on the action payload, so the
// work pin, the generated envelope, and the store validator read one
// declaration.
// CD-0198 D1 ships the record_verdict batch declaration: implementation 20,
// break_fix 18, research 12, architecture_spike 13, ops_runbook 14,
// static_analysis 11, and generic_one_off 12 make predicate_id optional
// beside the new verdicts array on the action payload; builtinActionPolicies
// stays byte-identical, so every released version above keeps its digest.
// CD-0198 D4 ships the accept_worker_result delivery declaration:
// implementation 21 and break_fix 19 add optional delivery_artifact and
// delivery_state beside the attempt identity, and the five families without
// a refinement step publish the same overlay at their next versions so the
// one action carries one contract; the guard that requires the fields at
// the delivery-admitting refinement step lives in the dispatcher, and every
// released version above keeps its digest.
// CD-0202 ships the record_proposal out_of_scope declaration:
// implementation 22 adds the optional out_of_scope prose list beside the
// shared policy table's fields; builtinActionPolicies stays byte-identical,
// so every released version above keeps its digest.
// The recovery-route declaration ships one new version per family:
// implementation 23, break_fix 20, research 14, architecture_spike 15,
// ops_runbook 16, static_analysis 13, and generic_one_off 14 declare the
// optional recovery_routes table. The omitempty field keeps every released
// manifest above byte-identical; the released versions resolve the same
// routes through workflowReleasedRecoveryRoutes until instances pin these.
// CON-887 ships the work-context action: implementation 26, break_fix 23,
// research 16, architecture_spike 17, ops_runbook 18, static_analysis 15,
// and generic_one_off 16 add record_work_context to every step except the
// closed delivery gates, whose four-action shape the gate reader pins.
// CON-890 ships the owner-level acceptance oracle on the two job-capable
// repair families: implementation 27 and break_fix 24 declare the required
// acceptance_oracle member on record_worker_job's payload. Every released
// version below keeps the payload it was pinned under, so oracle capability
// is a declared action member, never a behavior flag on a historical pin.
// The complete-step correction trigger ships in the same tables (CD-0172
// D3): implementation and break_fix carry a disproved_premise_at_complete
// route from the complete action step their graphs pin, and CD-0186 keeps
// the other five families outside that route.
//
// Editing a definition changes its computed digest and fails this test. Ship
// the new content as a new version and add its digest here; never edit a row
// that already holds a digest.
var workflowDefinitionVersionPins = map[[2]string]string{
	{"workflow.architecture_spike", "9"}:  "sha256:f7a4e6054573c2afaafea8293b298f9590bbfc7a6ede183fe00b6f58874a15e1",
	{"workflow.architecture_spike", "10"}: "sha256:940f65530981b1f56299c3f06329a00b0ef4c61a35beca62e05f2c6b576db133",
	{"workflow.architecture_spike", "8"}:  "sha256:4b39c8b14753ec0cdc9f66444a980f77cc802b60fafa343974483149c4c71758",
	{"workflow.architecture_spike", "6"}:  "sha256:7c734ff4bcb0c0f7881b0101779c576ff39535432148e031a7b984a28802fa83",
	{"workflow.architecture_spike", "7"}:  "sha256:420e3a67cfbc314ebbcc9ab921bdf4459f7a7df73bdea91c8aea327998302468",
	{"workflow.ops_runbook", "9"}:         "sha256:d3ecda770e1f818dee63f420d400a582d07ff6e77e7401bbc2754b9fd045fa9b",
	{"workflow.ops_runbook", "10"}:        "sha256:bc1e035506c499b0f1e05d7655a92254c32000cb24a9117da52b252da791bada",
	{"workflow.ops_runbook", "11"}:        "sha256:800de23f8a6b35f13b898366a1d2a3087ffabe6a05bcacc095c031a2b705cc36",
	{"workflow.ops_runbook", "7"}:         "sha256:5fbe2710a20464f0bb56167fb1c803d4956b5b1c804424a86014f6c67e6ccb03",
	{"workflow.ops_runbook", "8"}:         "sha256:d291c90b582c6e825590db9dcf789f7c8e35bb373c32f6b2430839943043bb1f",
	{"workflow.break_fix", "10"}:          "sha256:dad5a4de999f2a2f946702fe3f7aee012acda8fdc283d0a123f50662d6cf310d",
	{"workflow.break_fix", "11"}:          "sha256:2c13955de3099ab12072d1cc450f2fe37a99027903facc11690ad1ea42d2d7a8",
	{"workflow.break_fix", "12"}:          "sha256:ad0d90e5e531160fea75094a38dcbac00b4ca33bafdc346ea02ccfe2fe44f7d2",
	{"workflow.break_fix", "13"}:          "sha256:c11469369d6aa689997e0f3c801938fd05ee47d7b1eac37e46de91e26eba1573",
	{"workflow.break_fix", "14"}:          "sha256:c89bc73749f03062931776ffb4ba4cbdcbaae023cf0b9d8be3dd841885dbef36",
	{"workflow.break_fix", "1"}:           "sha256:aefce865f350345dc41fc1e2e988e7d5e246fa7fd560335399cf8c826e4cc35a",
	{"workflow.break_fix", "2"}:           "sha256:d7f8d8cc8b951e74751ddafe95c7b9c9d65e606cd73c41b2ceadd5fa2cdf29cb",
	{"workflow.break_fix", "3"}:           "sha256:3a406e35712a33dcab245ab93c77d51e0811ef51fbefb1a54c2fb40ae6b1f8b6",
	{"workflow.break_fix", "4"}:           "sha256:db759043452bf078754a547f4222d1adcc43b8b386af79cf81dfb517c39c4056",
	{"workflow.break_fix", "5"}:           "sha256:bb565fdcb7bcf4b4ba630ea1c56a2d34ac79ab17c486df63648775212b9047e1",
	{"workflow.break_fix", "6"}:           "sha256:ba1d605ecaa729f6ac729f6fc06733404875e2c45c90061c8d6c01b5e785eebc",
	{"workflow.break_fix", "7"}:           "sha256:7ba7c722550e96e45f98ffd6de41cf6fc8d14baca3d400abe9f8a68b700bc667",
	{"workflow.break_fix", "8"}:           "sha256:91a04421b1935cfb1d5600eda9a55b7c3791cc25527ba1007416d57ebae74368",
	{"workflow.break_fix", "9"}:           "sha256:d2e5be89e2f0b15e152709915c56f104d89a511aeadeb34b245bcc1f5004c177",
	{"workflow.implementation", "1"}:      "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155",
	{"workflow.implementation", "2"}:      "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89",
	{"workflow.implementation", "3"}:      "sha256:12ecaeb8b7947387905b0354f131308586635e61a553eba25deb1564179cbcd4",
	{"workflow.implementation", "4"}:      "sha256:454a0d11d32f0ca415a42306da65e0696b5884ee56a6ecbfba7771e5a8dc96ff",
	{"workflow.implementation", "5"}:      "sha256:64714294b727772997db3eb96e696b7c2b9cae64a01edc840e4fd9a9d5226661",
	{"workflow.implementation", "6"}:      "sha256:6f512e8f25772ef071d00f1378a8f675cac2624fe230c7c75a1e82635e2de785",
	{"workflow.implementation", "7"}:      "sha256:7b0e0582da3716e7d44a8097d16afa81deecf59b61d3af4c02833fa6f443291a",
	{"workflow.implementation", "8"}:      "sha256:15028d1f2b1d00df2ea0c0649f72dc9fb6151fb05bc88e3a6002eb5a93568f7c",
	{"workflow.implementation", "9"}:      "sha256:787ae3f02df30630408ef1c12badcd280eb604e71909fe7227b3c08b94cb71ca",
	{"workflow.implementation", "10"}:     "sha256:9335bd4830c5592240e578528582a26b51702f3f6d0b4f20916c47267fd160ac",
	{"workflow.implementation", "11"}:     "sha256:8a205e8b43f537caa650e72c667051017d9b26f77f675d1f5c14511743ef6462",
	{"workflow.implementation", "12"}:     "sha256:26340249dcc709a625db60711bc980acb2c8bdfb8be1588b1b1b80839b8d6f33",
	{"workflow.implementation", "13"}:     "sha256:a17d5afc3ee430aeaf406a004e93489daafea7138eb178c8e0ae63506496d341",
	{"workflow.implementation", "14"}:     "sha256:609beef3fda073435d22fcba81e9661b64979c77561d485e6c60268a243056cf",
	{"workflow.implementation", "15"}:     "sha256:e67484ab82967317470b819b1efe8c0af71448fa8a6c1eb1dd0b2989a4b5f996",
	{"workflow.implementation", "16"}:     "sha256:ac13f85d1e460c3f54be49e3550aeac564e510ed66db524ee10f619bd2ea05cc",
	{"workflow.implementation", "17"}:     "sha256:a50776df39e5a2a6c961b2a580bf320675eacf15d2d1fa0a9d9723d3911cde80",
	{"workflow.implementation", "18"}:     "sha256:0f114bc254d375e97f6c8bf702ce89550117e0d7d5b7c5af9a33927f435437db",
	{"workflow.break_fix", "15"}:          "sha256:dc4b081253f694fdd4383792b762b0b03517fa84256f04219c0a6fa15156162c",
	{"workflow.break_fix", "16"}:          "sha256:0f9377a09d89983b2d1769d5232eb27a23d8ea5f51d0745ff0a1675275256b37",
	{"workflow.research", "10"}:           "sha256:a128ff1052e9ccca96ddab042ead8cd323ef9b860ca291e002def58806349229",
	{"workflow.architecture_spike", "11"}: "sha256:70bc49deec7cc09ae69854822e11b2f4bdac5c58bae80813fb4c3f4e0aed51d5",
	{"workflow.ops_runbook", "12"}:        "sha256:478dae5af8bb382061e806353d53aa16d0b4047aa151b80ae6a3fb86c7798c25",
	{"workflow.static_analysis", "9"}:     "sha256:2b25171e70180d1c842ab9bfdae5e4299aa4463be01174b81399c9e73bd3ae56",
	{"workflow.generic_one_off", "10"}:    "sha256:77336c042ffb2fbd8e5ee7922d6c86399321af7966af61545688f5d53bec915c",
	{"workflow.generic_one_off", "7"}:     "sha256:11a397de86d2d4ef98acc345a2ed91a14b0c45c1a0054b140e40a45b9a6127b7",
	{"workflow.generic_one_off", "8"}:     "sha256:c5e07368f5d906a6c0996ea2041c488ce282fb9ce82f3e5f4acc559d4a0fec7e",
	{"workflow.generic_one_off", "9"}:     "sha256:b8b85e67bf052cdf90a774b9ff7f050fa15f814b4cbb7108df55525235bd46e0",
	{"workflow.generic_one_off", "1"}:     "sha256:c2b8b4c8ef11b2de08912f7c82faa91dffe6a2fbe4ddcef924ff4b393da578b3",
	{"workflow.generic_one_off", "2"}:     "sha256:273c82c0a0cf6c17d231f1be898ff74c6158f8036985cb3e1666b8f12c1b7895",
	{"workflow.generic_one_off", "3"}:     "sha256:a639d5a41e09ed2b2f1543912c4dc8686fb6d5cc97d945d4c3f6705df4f1165f",
	{"workflow.generic_one_off", "4"}:     "sha256:88f23cc8d70671d864cd8def4e100e1d029ffc6fdd4cbd2e4cc52f581958309c",
	{"workflow.generic_one_off", "5"}:     "sha256:9e98c10d52300d01ed1462ab48e3c880368c94a696072edd5301150e6d677b76",
	{"workflow.generic_one_off", "6"}:     "sha256:1db199cb3ac9967fc42f0105018e10c13ab96e87141381febfe45a819c550309",
	{"workflow.research", "1"}:            "sha256:adeb334ee4eb08e1907b2f36c618d809675a81f325266733142e697a90c108b9",
	{"workflow.research", "2"}:            "sha256:7a987b5e2cbc9bafd7a80e92345efa35b331ccaa533aefe4722024485be57e4a",
	{"workflow.research", "3"}:            "sha256:c8708cf194d2b5d4ac4421c3ee0eff09bc4074b5efe34bbab7ff546df0fd1256",
	{"workflow.research", "4"}:            "sha256:5f78eff3f315cf8b11a6489bba557666992b654bbcf15663fc3707ada0c52a0c",
	{"workflow.research", "5"}:            "sha256:256b8a088e395fd1ec5d263a35e3f60ee51b1465db58eb12f68ca4e0ef458209",
	{"workflow.research", "6"}:            "sha256:9510bd915cdd12d35c3c69c36e7834eb298cb03745a395c65d2c76da13d73a3d",
	{"workflow.research", "7"}:            "sha256:9398812339c38d67d70bf19abf2752541e84d7ebd29c4d6063f11a9408c9eeda",
	{"workflow.research", "8"}:            "sha256:47afbd04d70dadf08cecaccb964927580d034c5fee1c595feb4518c973ff2276",
	{"workflow.research", "9"}:            "sha256:502865be8c2805bbf7e33a133714df6790bf5fc68e8c10370d73d68fb63d98da",
	{"workflow.architecture_spike", "1"}:  "sha256:97d09dd24f80750dfa403ac2ccb9bf17b046cbd04981358b7ebaf1b1076aef5e",
	{"workflow.architecture_spike", "2"}:  "sha256:99ecb21ccfbf263d61f44c43730a2e0945b721082fd8c8a40e50df143b5fffcc",
	{"workflow.architecture_spike", "3"}:  "sha256:3b8d63a48c2bdcd008eb69dcf0322b0b1c40b0566ab0c4157283d9282789f720",
	{"workflow.architecture_spike", "4"}:  "sha256:878c4019adc0692571c0d23f1f28238248dfc0f2b12816340eb8458b205208f7",
	{"workflow.architecture_spike", "5"}:  "sha256:44b9bd043ac4f82ed5564114bc944cab5507de3bb193f51978b39ab07d940826",
	{"workflow.ops_runbook", "1"}:         "sha256:2e681414f079418a9aa2c7260832837d7ecde0e3974fdd602749d8125072dba6",
	{"workflow.ops_runbook", "2"}:         "sha256:8e14e680f37516f3c596058058f4982eee30bd47f2dbd5b2a79f32ff1bc832af",
	{"workflow.ops_runbook", "3"}:         "sha256:bdeeb5c08ee8eae101bca23fcf95a2ba8687af542a4551d6ff61f6449c07259f",
	{"workflow.ops_runbook", "4"}:         "sha256:eea14c43d682cc5846e405ea7467aac19468dcea74e75eab7b5fb1883c09653d",
	{"workflow.ops_runbook", "5"}:         "sha256:df99926053090bdbada3c8de55775b47dfc3c5f6725e9a20c46abca755930cf1",
	{"workflow.ops_runbook", "6"}:         "sha256:89d8bfd21c5ac708b4ba27491d9a56fa0fd13e008ec315f9eac0b1eecd269053",
	{"workflow.static_analysis", "1"}:     "sha256:dcd49187f4c5f3f3a54aa3a54cf28ce1eae5a4e093203ec761cf314b2b1bdd91",
	{"workflow.static_analysis", "2"}:     "sha256:9b3f7159c5346ee1d5d8f7a6f7ff21bfb3ae7babfb08243d7a0f49f16624f522",
	{"workflow.static_analysis", "3"}:     "sha256:fdad8adff22cff5e2d1d1bcf73aeb5748f7bf3a65f45cc58eda399ae3a365704",
	{"workflow.static_analysis", "4"}:     "sha256:cc9f3aaa93e5b3f145939624431c81970386ed5aed0d03d4dffb52c06bd53b26",
	{"workflow.static_analysis", "5"}:     "sha256:3fcb33421b001caa7900a42698812d83b53529abf7a4e22ff001fb8bb80421dc",
	{"workflow.static_analysis", "6"}:     "sha256:bb4eeec6f44116e19b47b1f910f19d5407c3964651be9595a1e519f9b9a54663",
	{"workflow.static_analysis", "7"}:     "sha256:e028a8ff55dd0517046c9620868506ba7ceb401e3ac0757a29cbbfe229782fd9",
	{"workflow.static_analysis", "8"}:     "sha256:b49180f83820b36f622e0e2426515f60a1d136e2c8ff0711609d6baa2cf4e462",
	{"workflow.implementation", "19"}:     "sha256:60bbb73777f24c3367c88b7fcdfb44cdd3e15b1a7ff6dd385ead60f522d38cb5",
	{"workflow.break_fix", "17"}:          "sha256:c8e15e64059f7cab5b619302ab8b398cd531974cbe56f2c36bc96d76d662c4fb",
	{"workflow.research", "11"}:           "sha256:71faedbac05a1b87d384fc1e965545c7877f4a47c86888ce090a2e41c6a6d03a",
	{"workflow.architecture_spike", "12"}: "sha256:c999ff5de3728335360220a83f2762dfbc31920d65c9d0c2d8ec22b749f84f5c",
	{"workflow.ops_runbook", "13"}:        "sha256:01546927bf054a86f869e2e597c33809c5e121d9d6e2e480032d52d83069ed28",
	{"workflow.static_analysis", "10"}:    "sha256:68089d6961fe7060beab9b853f8d6b40d63dea5aff22cce49d61d776ab9592af",
	{"workflow.generic_one_off", "11"}:    "sha256:9e70765c76ae2c95500de5ed712bb5997e074d17f17d3119e32618c970683840",
	{"workflow.implementation", "20"}:     "sha256:eb99ac7107790b9572b827481133ddcea50bd14085b2719e7b4e670910ac928c",
	{"workflow.break_fix", "18"}:          "sha256:9b355f4af052d0f48c71d0616177f8b0c2b0cd56bfdfda57a92ea8af46cfaad1",
	{"workflow.research", "12"}:           "sha256:3fa73bef80b56532fbc3c71d738aef597547dd3d37864f34eee2af073a0a2de9",
	{"workflow.architecture_spike", "13"}: "sha256:2546c5f66ca5f4b0997ee66c6d287403da58521b160a29f6051983f028879a9c",
	{"workflow.ops_runbook", "14"}:        "sha256:7cca47542e21e08d7224d8a03808b5980c4a1c5d40c305b9bd08b04319cfce2a",
	{"workflow.static_analysis", "11"}:    "sha256:a28a2eee538a025786a9d7b44943f5d6cafdb7ecfe252b0e0df2dd1cccb9dfe3",
	{"workflow.generic_one_off", "12"}:    "sha256:3aeef45c72b7c9b73062cf65ebac6482cf2d42201bdb0ed947f7ce92a35b4783",
	{"workflow.implementation", "21"}:     "sha256:0a6ed9d4f03954f8d8802b4c953cbec7338860f3552aa83bac4b97fcf469c901",
	{"workflow.implementation", "22"}:     "sha256:1bef12c2d072dae7bfcc65a2b097248678770481cc849bf79107cc9833b3b7f4",
	{"workflow.break_fix", "19"}:          "sha256:df5d441762daf131a1ae62c1abc9897f0269ebd3890322fff6c9a480ca769cc7",
	{"workflow.implementation", "24"}:     "sha256:2f5b7148662ffb007d1e489b74bda8ef51dfce45225c59b386292ba233312257",
	{"workflow.implementation", "25"}:     "sha256:82ee5b214f1ed53be13b20f29bb0ae5d8c7810ca8576599dc805d4abfd8cb261",
	{"workflow.break_fix", "21"}:          "sha256:9cae99acc96a249252a7c094e8b6958c470ca089dd8bc9e0fddd8a7430f39b85",
	{"workflow.break_fix", "22"}:          "sha256:edc03d0011ca9e967be5eb73e3336adcf5bfdd5d8125b319ee3e361e7ceb0b7c",
	{"workflow.research", "13"}:           "sha256:ec79e80293f12dd40fc11eac4f0413a4acd45cf76c0289436e7477b882cc3d9f",
	{"workflow.architecture_spike", "14"}: "sha256:4cafbb1659213f0d80faf45483be186d29fcc9577090d0382989a42aa437726b",
	{"workflow.ops_runbook", "15"}:        "sha256:547286502ff9c69fad47f5c880508a25803592f9a3befaa7700ca1541843255d",
	{"workflow.static_analysis", "12"}:    "sha256:a4764e6a87be89a141b7cb1fe16b1e90c25ece9c6ca4e53eb18e8aca47721748",
	{"workflow.generic_one_off", "13"}:    "sha256:964098ba2681f7fab8e1f67f90478af30ad7fb7442e6af14e9ac232d07825d0f",
	{"workflow.implementation", "23"}:     "sha256:01e95e83fbe78a4d5fc1d7a4f0c173c30b8225a62393b8a8270bfef9f6b0361b",
	{"workflow.break_fix", "20"}:          "sha256:ceaca11a0c5df7b3feaec11eed79616a06f5c1723d234fb8cd3cbe17bd787e1e",
	{"workflow.research", "14"}:           "sha256:4fbb43fd5ecdff2d8625584dc8e99689982a842396b3ed86bb67c2c9ab6ccb0b",
	{"workflow.research", "15"}:           "sha256:c9b0c5a8219c846322f0cc36a38de46f47765591ca4b1a29abcf72e36abe897f",
	{"workflow.architecture_spike", "15"}: "sha256:b8b3397d7918be844bd847e429ec5a224cf1bfe3683ef4f839077c06453bc38d",
	{"workflow.architecture_spike", "16"}: "sha256:db298cde3c4818f873cb41e27935d3ee404273832e788212aedf3592bb1f0ba7",
	{"workflow.ops_runbook", "16"}:        "sha256:561dfbf45762ae9ec6deca2bf599a2cd79b3befa0e521f33fb59565b827054d1",
	{"workflow.ops_runbook", "17"}:        "sha256:6115e9c12d7ee98c1ab1f6d311eafe2eb7b8f9f1f1d577a61b7571471d6f08a0",
	{"workflow.static_analysis", "13"}:    "sha256:ee5b49431e30dca99e60c651aa7de54f4f99fae71aa927baf034bf90fecce8ae",
	{"workflow.static_analysis", "14"}:    "sha256:9dcc6f432eb1da4963b86e8494a20aeabc49c0b5ac459838a80704e145284299",
	{"workflow.generic_one_off", "14"}:    "sha256:727d5743776421aa55fc0a196412bdd57005cec6e3a1125d523d0cc27d9e79b2",
	{"workflow.generic_one_off", "15"}:    "sha256:263e045c468d31248a33e3f563c7703707a0428e7160cf2e28cbacdd9b8f0abe",
	{"workflow.implementation", "26"}:     "sha256:c4a5623f94eda4ac4578462b54b84f4475997780b3e75c457367971196c02ad1",
	{"workflow.implementation", "27"}:     "sha256:a4510246b78d843cc27efad6d09f290c057dc2fc9b21f4a92a6ad8d1c9a42252",
	{"workflow.break_fix", "23"}:          "sha256:abd2369cdb1614c52cfe54eab9f3040e87d37ffcf70c232d1c5088e9d92f42e0",
	{"workflow.break_fix", "24"}:          "sha256:6e4445beac1027d110bc9512a7961ee9ecdac55054f10e861fdcc89c0a056137",
	{"workflow.research", "16"}:           "sha256:df5941b03aaf8a47af83bb070b749ae322974ed026b2d9cf44f9a066eeff5018",
	{"workflow.architecture_spike", "17"}: "sha256:3e9d434cba63d47aadea18399c3f950b89e93a5cc69317b8685fbbee2d245e55",
	{"workflow.ops_runbook", "18"}:        "sha256:354390116b3faf7a73524ca4bfbbfe962650f8ed8eaf73a68845d812daade63a",
	{"workflow.static_analysis", "15"}:    "sha256:5a561e9d53d26fc0ddec0f84aa72ca5cd2a0018e92dfa5dd0732efd0a8b32ade",
	{"workflow.generic_one_off", "16"}:    "sha256:05e53c76e3f6c712cc62f7421c22a3df4719767aba91f7a07049ba973190d8d7",
}

// TestWorkflowDefinitionDraftVersionDigests prints only the unreleased
// definitions whose digest pins must be recorded after composing their content.
// Run with: bin/oc-test targeted -- go test ./internal/store -run '^TestWorkflowDefinitionDraftVersionDigests$' -v -count=1 -timeout=180s
func TestWorkflowDefinitionDraftVersionDigests(t *testing.T) {
	for _, definition := range []WorkflowDefinition{
		implementationWorkContextV26(), implementationOwnerOracleV27(),
		breakFixWorkContextV23(), breakFixOwnerOracleV24(),
		researchWorkContextV16(), architectureWorkContextV17(),
		opsRunbookWorkContextV18(), staticAnalysisWorkContextV15(), genericOneOffWorkContextV16(),
	} {
		digest, err := WorkflowDefinitionDigest(definition)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s v%d %s", definition.Ref, definition.Version, digest)
	}
}

func TestWorkflowDefinitionDraftPromotionRetainsGaplessPins(t *testing.T) {
	t.Run("pins", TestBuiltinDefinitionsCoverExactlyThePinnedVersions)
	t.Run("gapless", TestBuiltinDefinitionVersionsAreGapless)
	t.Run("latest", TestBuiltinDefinitionForRefResolvesTheLatestVersion)
}

func TestWorkflowDefinitionVersionPinsHold(t *testing.T) {
	t.Parallel()
	for pin, digest := range workflowDefinitionVersionPins {
		ref, version := pin[0], pin[1]
		entry, ok := BuiltinWorkflowRegistry().Lookup(ref, pinVersion(t, version))
		if !ok {
			t.Errorf("%s version %s is not registered", ref, version)
			continue
		}
		computed, err := WorkflowDefinitionDigest(entry.Definition)
		if err != nil {
			t.Errorf("%s version %s digest cannot be computed: %v", ref, version, err)
			continue
		}
		if entry.Digest != digest || computed != digest {
			t.Errorf("%s version %s digest drifted: registered %s computed %s pinned %s — ship changed content as a new version instead of editing a released one", ref, version, entry.Digest, computed, digest)
		}
		if err := BuiltinWorkflowRegistry().Verify(ref, pinVersion(t, version), digest); err != nil {
			t.Errorf("%s version %s pin does not verify: %v", ref, version, err)
		}
	}
}

func TestBuiltinDefinitionsCoverExactlyThePinnedVersions(t *testing.T) {
	t.Parallel()
	seen := map[[2]string]bool{}
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		pin := [2]string{definition.Ref, versionString(definition.Version)}
		if seen[pin] {
			t.Errorf("%s version %s is registered twice", definition.Ref, versionString(definition.Version))
		}
		seen[pin] = true
		if _, held := workflowDefinitionVersionPins[pin]; !held {
			t.Errorf("%s version %s is registered but holds no digest pin; add it to workflowDefinitionVersionPins", definition.Ref, versionString(definition.Version))
		}
	}
	for pin := range workflowDefinitionVersionPins {
		if !seen[pin] {
			t.Errorf("%s version %s holds a digest pin but is not registered", pin[0], pin[1])
		}
	}
}

func TestBuiltinDefinitionVersionsAreGapless(t *testing.T) {
	t.Parallel()
	if err := validateBuiltinWorkflowVersionContinuity(builtinWorkflowDefinitionsWithHistory()); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowVerdictSingleFormAcrossPinnedAndBatchVersions holds the payload
// boundary CD-0198 D1 promises: the released versions keep the single-form
// record_verdict declaration they were pinned under and refuse the batched
// field, while the batched versions keep every single-form field with
// predicate_id optional beside the verdicts array.
func TestWorkflowVerdictSingleFormAcrossPinnedAndBatchVersions(t *testing.T) {
	t.Parallel()
	pinned, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 19)
	if !ok {
		t.Fatal("workflow.implementation v19 is not registered")
	}
	var pinnedPredicate *WorkflowPayloadField
	for index := range pinned.Definition.ActionDefinitions {
		if pinned.Definition.ActionDefinitions[index].ID != "record_verdict" {
			continue
		}
		for fieldIndex := range pinned.Definition.ActionDefinitions[index].Payload.Fields {
			field := &pinned.Definition.ActionDefinitions[index].Payload.Fields[fieldIndex]
			if field.Name == "predicate_id" {
				pinnedPredicate = field
			}
			if field.Name == "verdicts" {
				t.Fatal("pinned implementation v19 declares the batched verdicts field; a released version changed under its pin")
			}
		}
	}
	if pinnedPredicate == nil || !pinnedPredicate.Required {
		t.Fatal("pinned implementation v19 does not require predicate_id on record_verdict")
	}
	singleForm := []byte(`{"contract_version":1,"predicate_id":"predicate:one","verdict_kind":"ok","evaluation_evidence":["evidence:one"]}`)
	if err := validateWorkflowActionPayload(pinned.Definition, "record_verdict", singleForm); err != nil {
		t.Fatalf("single-form verdict against the pinned version refused: %v", err)
	}
	batchedForm := []byte(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:one"}]}`)
	if err := validateWorkflowActionPayload(pinned.Definition, "record_verdict", batchedForm); err == nil {
		t.Fatal("the pinned version admitted the batched verdicts field; pinned instances would change behavior")
	}

	current := implementationVerdictBatchV20()
	var predicate *WorkflowPayloadField
	var verdicts *WorkflowPayloadField
	for index := range current.ActionDefinitions {
		if current.ActionDefinitions[index].ID != "record_verdict" {
			continue
		}
		for fieldIndex := range current.ActionDefinitions[index].Payload.Fields {
			field := &current.ActionDefinitions[index].Payload.Fields[fieldIndex]
			switch field.Name {
			case "predicate_id":
				predicate = field
			case "verdicts":
				verdicts = field
			}
		}
	}
	if predicate == nil || predicate.Required {
		t.Fatal("the batched version must keep predicate_id declared and optional")
	}
	if verdicts == nil || verdicts.ValueType != PayloadArray || verdicts.ItemRef != "workflow_verdict_batch_entry" || *verdicts.MinItems != 1 || *verdicts.MaxItems != 8 {
		t.Fatalf("the batched verdicts declaration = %+v, want a 1..8 array of workflow_verdict_batch_entry items", verdicts)
	}
	if err := validateWorkflowActionPayload(current, "record_verdict", singleForm); err != nil {
		t.Fatalf("single-form verdict against the batched version refused: %v", err)
	}
	if err := validateWorkflowActionPayload(current, "record_verdict", batchedForm); err != nil {
		t.Fatalf("batched verdict against the batched version refused: %v", err)
	}
	// The store refuses the cross-field shapes the flat field list cannot
	// state: a call with both forms, neither, or an entry-level field
	// beside the batch refuses through the engine cross-field declaration
	// the preflight enforces and publication branches from (CON-412);
	// normalizeWorkflowVerdictEntries only decodes the wire shapes.
	both := []byte(`{"contract_version":1,"predicate_id":"predicate:one","verdicts":[{"predicate_id":"predicate:one"}]}`)
	if err := validateWorkflowActionPayload(current, "record_verdict", both); err == nil {
		t.Fatal("validation admitted predicate_id beside verdicts")
	}
	neither := []byte(`{"contract_version":1,"verdict_kind":"ok"}`)
	if err := validateWorkflowActionPayload(current, "record_verdict", neither); err == nil {
		t.Fatal("validation admitted a call with neither form")
	}
	bothAndEntry := []byte(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:one"}],"evaluation_evidence":["evidence:one"]}`)
	if err := validateWorkflowActionPayload(current, "record_verdict", bothAndEntry); err == nil {
		t.Fatal("validation admitted an entry-level field beside the batch")
	}
	if err := validateWorkflowActionPayload(pinned.Definition, "record_verdict", singleForm); err != nil {
		t.Fatalf("single-form verdict against the pinned version refused: %v", err)
	}
}

func TestBuiltinDefinitionVersionContinuityRejectsGap(t *testing.T) {
	t.Parallel()
	definitions := builtinWorkflowDefinitionsWithHistory()
	for index, definition := range definitions {
		if definition.Ref != "workflow.implementation" || definition.Version != 14 {
			continue
		}
		definitions = append(definitions[:index], definitions[index+1:]...)
		break
	}
	if err := validateBuiltinWorkflowVersionContinuity(definitions); err == nil {
		t.Fatal("version continuity check accepted a missing built-in version")
	}
}

func TestBuiltinDefinitionForRefResolvesTheLatestVersion(t *testing.T) {
	t.Parallel()
	cases := map[string]int64{
		"workflow.break_fix":          24,
		"workflow.implementation":     27,
		"workflow.generic_one_off":    16,
		"workflow.research":           16,
		"workflow.architecture_spike": 17,
		"workflow.ops_runbook":        18,
		"workflow.static_analysis":    15,
	}
	for ref, version := range cases {
		registered, err := BuiltinWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Errorf("%s does not resolve: %v", ref, err)
			continue
		}
		if registered.Definition.Version != version {
			t.Errorf("%s resolved version %d, want %d", ref, registered.Definition.Version, version)
		}
	}
}

func pinVersion(t *testing.T, value string) int64 {
	t.Helper()
	var parsed int64
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		t.Fatalf("version %q is not a number: %v", value, err)
	}
	return parsed
}

func versionString(value int64) string {
	return fmt.Sprintf("%d", value)
}
