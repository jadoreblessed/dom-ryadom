package main

func responsibleFor(category string, building Building) string {
	switch category {
	case "Протечка":
		return "Аварийно-диспетчерская служба"
	case "Нет отопления":
		return building.ManagingOrg
	case "Не работает лифт":
		return building.ManagingOrg + " (диспетчер лифта)"
	case "Нет освещения":
		return building.ManagingOrg
	default:
		return "Требуется уточнение проблемы"
	}
}
func nextStepFor(category string) string {
	switch category {
	case "Протечка":
		return "Свяжитесь с аварийно-диспетчерской службой вашего дома и сообщите адрес и место протечки."
	case "Нет отопления":
		return "Сообщите в управляющую организацию адрес и время, когда пропало отопление."
	case "Не работает лифт":
		return "Сообщите о неисправности в диспетчерскую службу, указанную для вашего дома."
	case "Нет освещения":
		return "Уточните место неисправности и сообщите в управляющую организацию."
	default:
		return "Уточните описание проблемы, чтобы определить ответственного."
	}
}
func isValidCategory(category string) bool {
	switch category {
	case "Протечка",
		"Нет отопления",
		"Не работает лифт",
		"Нет освещения",
		"Другое":
		return true
	default:
		return false
	}
}
